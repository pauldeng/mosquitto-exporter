package exporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	mqttConnectTimeout = 3 * time.Second
)

// buildTLSConfig assembles the broker TLS configuration, or returns (nil,
// nil) when no TLS-related flag was set at all - in which case an ssl://
// endpoint still gets a secure, verified default TLS handshake from paho
// (nil *tls.Config in Go's crypto/tls means "verify against the system CA
// pool"), and a tcp:// endpoint is unaffected.
//
// Fails closed (S2): any load error aborts startup instead of continuing
// with a partial or zero-value TLS configuration.
func buildTLSConfig(certFile, keyFile, caCertFile string, insecureSkipVerify bool) (*tls.Config, error) {
	if certFile == "" && keyFile == "" && caCertFile == "" && !insecureSkipVerify {
		return nil, nil
	}

	// MinVersion pinned per gosec G402: never negotiate below TLS 1.2.
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	switch {
	case certFile != "" && keyFile != "":
		keyPair, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("loading TLS keypair (%s, %s): %w", certFile, keyFile, err)
		}
		tlsConfig.Certificates = []tls.Certificate{keyPair}
	case certFile != "" || keyFile != "":
		return nil, errors.New("both --cert and --key are required for TLS client authentication, only one was set")
	}

	if caCertFile != "" {
		//nolint:gosec // G304: caCertFile is an operator-supplied path, the entire point of --ca-cert
		pemBytes, err := os.ReadFile(caCertFile)
		if err != nil {
			return nil, fmt.Errorf("reading --ca-cert %s: %w", caCertFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, fmt.Errorf("no valid certificates found in --ca-cert %s", caCertFile)
		}
		tlsConfig.RootCAs = pool
	}

	if insecureSkipVerify {
		slog.Warn("TLS certificate verification disabled via --insecure-skip-verify: the broker's identity will not be authenticated")
		tlsConfig.InsecureSkipVerify = true
	}

	return tlsConfig, nil
}

// connectWithBackoff makes the initial connection attempt to the broker,
// retrying with exponential backoff and jitter until it succeeds or ctx is
// cancelled (R6). Once connected, paho's own AutoReconnect (left at its
// default) takes over for any later, post-initial connection loss.
//
// Logging is rate-limited: every attempt for the first few, then every 10th,
// so a broker that stays down for a long time does not flood the logs.
func connectWithBackoff(ctx context.Context, client mqtt.Client, endpoint string) {
	const (
		initialBackoff = 1 * time.Second
		maxBackoff     = 60 * time.Second
	)

	backoff := initialBackoff
	for attempt := 1; ; attempt++ {
		token := client.Connect()
		gotResult := token.WaitTimeout(mqttConnectTimeout)

		if ctx.Err() != nil {
			return
		}
		if gotResult && token.Error() == nil {
			slog.Info("connected to broker", "endpoint", endpoint)
			return
		}

		err := token.Error()
		if err == nil {
			err = fmt.Errorf("timeout connecting to %s", endpoint)
		}
		if attempt <= 5 || attempt%10 == 0 {
			slog.Warn("failed to connect to broker", "endpoint", endpoint, "attempt", attempt, "error", err, "retry_in", backoff)
		}

		// jitter in [backoff/2, backoff) avoids every retrying instance
		// waking up in lockstep against the same broker.
		//nolint:gosec // G404: math/rand is fine for retry jitter, not a security decision
		wait := backoff/2 + time.Duration(rand.Int63n(int64(backoff)/2+1))
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// newHTTPServer builds the metrics HTTP server on its own explicit
// http.Server (S6) with all four timeouts set (S4 / gosec G114) - a
// zero-value http.Server has none of them and is vulnerable to Slowloris.
func newHTTPServer(bindAddress string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              bindAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

func Run(ctx context.Context, cfg config.Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	slog.Info("starting", "app", "mosquitto-exporter", "version", cfg.Version)

	// R4: SIGINT/SIGTERM cancel ctx, which drives both the connect-retry loop
	// and the shutdown sequence below.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := cfg.Endpoint
	tlsConfig, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CACertFile, cfg.InsecureSkipVerify)
	if err != nil {
		return fmt.Errorf("configuring TLS: %w", err)
	}

	store := NewStore()
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		store,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	opts := mqtt.NewClientOptions()
	opts.SetCleanSession(true)
	opts.SetConnectTimeout(mqttConnectTimeout)
	opts.Dialer.Timeout = mqttConnectTimeout
	opts.AddBroker(endpoint)

	if clientID := cfg.ClientID; clientID != "" {
		opts.SetClientID(clientID)
	}
	if user := cfg.User; user != "" {
		opts.SetUsername(user)
		if cfg.Password != "" {
			opts.SetPassword(cfg.Password)
		}
	}
	if tlsConfig != nil {
		opts.SetTLSConfig(tlsConfig)
		if !strings.HasPrefix(endpoint, "ssl://") && !strings.HasPrefix(endpoint, "tls://") {
			slog.Warn("TLS was configured but the endpoint scheme is not 'ssl://' or 'tls://'; it will be ignored")
		}
	}

	resetOnDisconnect := cfg.ResetMetrics
	opts.OnConnect = func(client mqtt.Client) {
		slog.Info("connected to broker", "endpoint", endpoint)
		store.SetConnected(true)
		// subscribe on every (re)connect
		token := client.Subscribe("$SYS/#", 0, func(_ mqtt.Client, msg mqtt.Message) {
			store.Update(msg.Topic(), string(msg.Payload()))
		})
		if !token.WaitTimeout(10 * time.Second) {
			slog.Error("timeout subscribing to topic", "topic", "$SYS/#")
		} else if err := token.Error(); err != nil {
			slog.Error("failed to subscribe to topic", "topic", "$SYS/#", "error", err)
		}
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		store.SetConnected(false)
		slog.Warn("connection to broker lost", "endpoint", endpoint, "error", err)
		if resetOnDisconnect {
			store.Reset()
		}
	}

	client := mqtt.NewClient(opts)

	// S6: an explicit ServeMux instead of http.DefaultServeMux's global
	// mutable state.
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))
	mux.HandleFunc("/", serveVersion(cfg.Version))

	srv := newHTTPServer(cfg.BindAddress, mux)

	serveErrCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "address", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErrCh <- err
			return
		}
		serveErrCh <- nil
	}()

	// R1: connect to the broker in the background so /metrics is already
	// serving (with mosquitto_up 0) while the broker is unreachable, instead
	// of refusing connections until the broker answers.
	connectDone := make(chan struct{})
	go func() {
		defer close(connectDone)
		connectWithBackoff(ctx, client, endpoint)
	}()

	var runErr error
	select {
	case <-ctx.Done():
		slog.Info("shutting down")
	case runErr = <-serveErrCh:
		// The HTTP server died on its own (e.g. address already in use):
		// stop the connect-retry goroutine too instead of leaking it.
		stop()
	}

	// Disconnect also cancels Paho's connecting/reconnecting state. Checking
	// IsConnected first would skip exactly the broker-down shutdown path.
	client.Disconnect(250)
	select {
	case <-connectDone:
	case <-time.After(mqttConnectTimeout + time.Second):
		if runErr == nil {
			runErr = errors.New("timed out stopping MQTT connection attempt")
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutting down HTTP server: %w", err)
	}

	return runErr
}
