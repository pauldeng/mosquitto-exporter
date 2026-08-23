package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/urfave/cli/v3"
)

const (
	appName            = "mosquitto-exporter"
	mqttConnectTimeout = 3 * time.Second
)

// logLevel backs the default logger's level and is set from --log-level /
// LOG_LEVEL once flags are parsed (see newCommand's Before hook). Its zero
// value is slog.LevelInfo, so logging before that point (e.g. a flag parse
// error) still uses the documented default.
var logLevel = new(slog.LevelVar)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

// parseLogLevel validates --log-level against slog's own level parsing
// (debug|info|warn|error, case-insensitive) rather than hand-rolling a
// second parser for the same four names.
func parseLogLevel(s string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(s)); err != nil {
		return 0, fmt.Errorf("invalid --log-level %q: %w", s, err)
	}
	return level, nil
}

func main() {
	if err := newCommand().Run(context.Background(), os.Args); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func newCommand() *cli.Command {
	return &cli.Command{
		Name:    appName,
		Version: versionString(),
		Authors: []any{
			"Johan Ryberg <johan@securit.se>",
			"Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>",
			"Fabian Ruff <fabian.ruff@sap.com>",
		},
		Usage: "Prometheus exporter for broker metrics",
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			level, err := parseLogLevel(cmd.String("log-level"))
			if err != nil {
				return ctx, err
			}
			logLevel.Set(level)
			return ctx, nil
		},
		Action: runServer,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "log-level",
				Usage:   "Log level (debug|info|warn|error)",
				Value:   "info",
				Sources: cli.EnvVars("LOG_LEVEL"),
			},
			&cli.StringFlag{
				Name:    "endpoint",
				Aliases: []string{"e"},
				Usage:   "Endpoint for the Mosquitto message broker",
				Sources: cli.EnvVars("BROKER_ENDPOINT"),
				Value:   "tcp://127.0.0.1:1883",
			},
			&cli.StringFlag{
				Name:    "bind-address",
				Aliases: []string{"b"},
				Usage:   "Listen address for metrics HTTP endpoint",
				Value:   "0.0.0.0:9234",
				Sources: cli.EnvVars("BIND_ADDRESS"),
			},
			&cli.StringFlag{
				Name:    "user",
				Aliases: []string{"u"},
				Usage:   "Username for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_USER"),
			},
			&cli.StringFlag{
				Name:  "pass-file",
				Usage: "Path to a file containing the password for the Mosquitto broker user (password can also come from MQTT_PASS; never pass it on argv)",
				Value: "",
				// Deliberately no env alias for MQTT_PASS here: reading MQTT_PASS
				// via os.Getenv directly (see resolvePassword) so no CLI flag ever
				// exists that would accept a password on argv (S3, decision 10.14).
				Sources: cli.EnvVars("MQTT_PASS_FILE"),
			},
			&cli.StringFlag{
				Name:    "cert",
				Aliases: []string{"c"},
				Usage:   "Location of a TLS certificate .pem file for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_CERT"),
			},
			&cli.StringFlag{
				Name:    "key",
				Aliases: []string{"k"},
				Usage:   "Location of a TLS private key .pem file for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_KEY"),
			},
			&cli.StringFlag{
				Name:    "ca-cert",
				Usage:   "Location of a PEM-encoded CA certificate used to verify the broker's TLS certificate",
				Value:   "",
				Sources: cli.EnvVars("MQTT_CA_CERT"),
			},
			&cli.BoolFlag{
				Name:    "insecure-skip-verify",
				Usage:   "Disable TLS certificate verification of the broker (INSECURE - only for testing)",
				Value:   false,
				Sources: cli.EnvVars("MQTT_INSECURE_SKIP_VERIFY"),
			},
			&cli.StringFlag{
				Name:    "client-id",
				Aliases: []string{"i"},
				Usage:   "Client id to be used to connect to the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_CLIENT_ID"),
			},
			&cli.BoolFlag{
				Name:    "reset-metrics",
				Aliases: []string{"r"},
				Usage:   "Reset metrics when losing connection to broker",
				Value:   true,
				Sources: cli.EnvVars("RESET_METRICS"),
			},
		},
	}
}

// validateEndpoint rejects a broker endpoint carrying embedded credentials
// (e.g. tcp://user:pass@host), which would otherwise leak through `ps`,
// shell history, and any log line that includes the endpoint (S3, decision
// 10.14). Credentials belong only in --user / --pass-file / MQTT_PASS.
func validateEndpoint(endpoint string) error {
	u, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", endpoint, err)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint must not contain userinfo (user:pass@host); use --user/--pass-file or MQTT_USER/MQTT_PASS instead")
	}
	return nil
}

// resolvePassword reads the broker password from --pass-file, falling back
// to the MQTT_PASS environment variable. There is deliberately no --pass
// flag: a broker password must never be accepted on argv (S3).
func resolvePassword(passFile string) (string, error) {
	if passFile == "" {
		return os.Getenv("MQTT_PASS"), nil
	}
	//nolint:gosec // G304: passFile is an operator-supplied path, the entire point of --pass-file
	data, err := os.ReadFile(passFile)
	if err != nil {
		return "", fmt.Errorf("reading --pass-file %s: %w", passFile, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

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

func runServer(ctx context.Context, cmd *cli.Command) error {
	slog.Info("starting", "app", appName, "version", versionString())

	// R4: SIGINT/SIGTERM cancel ctx, which drives both the connect-retry loop
	// and the shutdown sequence below.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := cmd.String("endpoint")
	if err := validateEndpoint(endpoint); err != nil {
		return err
	}

	password, err := resolvePassword(cmd.String("pass-file"))
	if err != nil {
		return err
	}

	tlsConfig, err := buildTLSConfig(cmd.String("cert"), cmd.String("key"), cmd.String("ca-cert"), cmd.Bool("insecure-skip-verify"))
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

	if clientID := cmd.String("client-id"); clientID != "" {
		opts.SetClientID(clientID)
	}
	if user := cmd.String("user"); user != "" {
		opts.SetUsername(user)
		if password != "" {
			opts.SetPassword(password)
		}
	}
	if tlsConfig != nil {
		opts.SetTLSConfig(tlsConfig)
		if !strings.HasPrefix(endpoint, "ssl://") && !strings.HasPrefix(endpoint, "tls://") {
			slog.Warn("TLS was configured but the endpoint scheme is not 'ssl://' or 'tls://'; it will be ignored")
		}
	}

	resetOnDisconnect := cmd.Bool("reset-metrics")
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
	mux.HandleFunc("/", serveVersion)

	srv := newHTTPServer(cmd.String("bind-address"), mux)

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
