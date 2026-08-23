package exporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"golang.org/x/net/proxy"
)

const (
	mqttConnectTimeout   = 3 * time.Second
	mqttSubscribeTimeout = 10 * time.Second
	mqttSubscribeRetry   = 10 * time.Second
	maxMQTTPacketBytes   = 64 << 10
	maxMetricsRequests   = 5

	// otelMeterName identifies the one Meter this exporter ever creates.
	// WithoutScopeInfo (otelprom.go) suppresses it from /metrics entirely, so
	// its exact value has no effect on exported output - it exists only for
	// anyone reading OTel-internal debug logs.
	otelMeterName = "github.com/pauldeng/mosquitto-exporter"
)

const (
	mqttReadHeader = iota
	mqttReadLength
	mqttReadPayload
)

// mqttPacketLimitConn rejects an oversized MQTT Remaining Length before Paho
// allocates the packet body. Paho reads the fixed header and length one byte at
// a time, so enforcing the limit here closes the allocation boundary that an
// application-level payload check cannot reach.
type mqttPacketLimitConn struct {
	net.Conn
	state      int
	remaining  int
	multiplier int
}

func (c *mqttPacketLimitConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	switch c.state {
	case mqttReadHeader:
		n, err := c.Conn.Read(p[:1])
		if n == 1 {
			c.state, c.remaining, c.multiplier = mqttReadLength, 0, 1
		}
		return n, err
	case mqttReadLength:
		n, err := c.Conn.Read(p[:1])
		if n != 1 {
			return n, err
		}
		digit := int(p[0] & 127)
		if c.multiplier > 128*128*128 || digit > (maxMQTTPacketBytes-c.remaining)/c.multiplier {
			return 0, fmt.Errorf("MQTT packet exceeds %d-byte limit", maxMQTTPacketBytes)
		}
		c.remaining += digit * c.multiplier
		if p[0]&128 != 0 {
			c.multiplier *= 128
			return 1, err
		}
		if c.remaining == 0 {
			c.state = mqttReadHeader
		} else {
			c.state = mqttReadPayload
		}
		return 1, err
	default:
		if len(p) > c.remaining {
			p = p[:c.remaining]
		}
		n, err := c.Conn.Read(p)
		c.remaining -= n
		if c.remaining == 0 {
			c.state = mqttReadHeader
		}
		return n, err
	}
}

func openLimitedMQTTConnection(uri *url.URL, options mqtt.ClientOptions) (net.Conn, error) {
	dialer := options.Dialer
	if dialer == nil {
		dialer = &net.Dialer{Timeout: mqttConnectTimeout}
	}
	var conn net.Conn
	var err error
	switch strings.ToLower(uri.Scheme) {
	case "ws", "wss":
		tlsConfig := options.TLSConfig
		if strings.EqualFold(uri.Scheme, "ws") {
			tlsConfig = nil
		}
		conn, err = mqtt.NewWebsocket(uri.String(), tlsConfig, options.ConnectTimeout, options.HTTPHeaders, options.WebsocketOptions)
	case "mqtt", "tcp":
		conn, err = proxy.FromEnvironmentUsing(dialer).Dial("tcp", uri.Host)
	case "unix":
		address := uri.Path
		if uri.Host != "" {
			address = uri.Host
		}
		conn, err = dialer.Dial("unix", address)
	case "ssl", "tls", "mqtts", "mqtt+ssl", "tcps":
		tlsConfig := options.TLSConfig
		if tlsConfig == nil {
			tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			tlsConfig = tlsConfig.Clone()
		}
		if tlsConfig.ServerName == "" {
			tlsConfig.ServerName = uri.Hostname()
		}
		if os.Getenv("all_proxy") == "" {
			conn, err = tls.DialWithDialer(dialer, "tcp", uri.Host, tlsConfig)
		} else {
			var raw net.Conn
			raw, err = proxy.FromEnvironment().Dial("tcp", uri.Host)
			if err == nil {
				tlsConn := tls.Client(raw, tlsConfig)
				err = tlsConn.Handshake()
				if err != nil {
					_ = raw.Close()
				} else {
					conn = tlsConn
				}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported MQTT endpoint scheme %q", uri.Scheme)
	}
	if err != nil {
		return nil, err
	}
	return &mqttPacketLimitConn{Conn: conn}, nil
}

// buildTLSConfig assembles the broker TLS configuration, or returns (nil,
// nil) when no TLS-related flag was set at all - in which case an ssl://
// endpoint still gets a secure, verified default TLS handshake from paho
// (nil *tls.Config in Go's crypto/tls means "verify against the system CA
// pool"), and a tcp:// endpoint is unaffected.
//
// Any load error aborts startup instead of continuing with a partial TLS
// configuration.
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
// cancelled. Once connected, paho's own AutoReconnect (left at its
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

type mqttSubscriber interface {
	IsConnected() bool
	Subscribe(string, byte, mqtt.MessageHandler) mqtt.Token
}

func retrySYSSubscription(client mqttSubscriber, store *Store, stillCurrent func() bool, retryDelay time.Duration) {
	for client.IsConnected() && stillCurrent() {
		token := client.Subscribe("$SYS/#", 0, func(_ mqtt.Client, msg mqtt.Message) {
			store.Update(msg.Topic(), string(msg.Payload()))
		})
		if token.WaitTimeout(mqttSubscribeTimeout) && token.Error() == nil {
			slog.Info("subscribed to broker metrics", "topic", "$SYS/#")
			return
		}
		if err := token.Error(); err != nil {
			slog.Error("failed to subscribe to broker metrics; retrying", "topic", "$SYS/#", "error", err)
		} else {
			slog.Error("timeout subscribing to broker metrics; retrying", "topic", "$SYS/#")
		}
		time.Sleep(retryDelay)
	}
}

func newMetricsHandler(gatherer prometheus.Gatherer) http.Handler {
	return promhttp.HandlerFor(gatherer, promhttp.HandlerOpts{
		MaxRequestsInFlight: maxMetricsRequests,
		CoalesceGather:      true,
	})
}

// newHTTPServer builds the metrics HTTP server on its own explicit
// http.Server with all four timeouts set; a
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

	// SIGINT/SIGTERM cancel ctx, driving connection retry and shutdown.
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	endpoint := cfg.Endpoint
	tlsConfig, err := buildTLSConfig(cfg.CertFile, cfg.KeyFile, cfg.CACertFile, cfg.InsecureSkipVerify)
	if err != nil {
		return fmt.Errorf("configuring TLS: %w", err)
	}

	store := NewStore(cfg.SysInterval)
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		store,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	// One instrument set feeds the production Prometheus reader and, when
	// explicitly configured, the experimental OTLP push reader.
	promReader, err := newPrometheusReader(registry)
	if err != nil {
		return fmt.Errorf("setting up OTel metrics: %w", err)
	}
	meterProviderOpts := []sdkmetric.Option{sdkmetric.WithReader(promReader)}
	if cfg.OTLPEndpoint != "" {
		otlpReader, otlpErr := newOTLPReader(cfg)
		if otlpErr != nil {
			return fmt.Errorf("setting up OTLP metrics: %w", otlpErr)
		}
		meterProviderOpts = append(meterProviderOpts, sdkmetric.WithReader(otlpReader))
	}
	meterProvider := sdkmetric.NewMeterProvider(meterProviderOpts...)
	if err := registerBrokerMetrics(meterProvider.Meter(otelMeterName), store); err != nil {
		_ = meterProvider.Shutdown(context.Background())
		return fmt.Errorf("setting up broker metrics: %w", err)
	}

	opts := mqtt.NewClientOptions()
	opts.SetCleanSession(true)
	opts.SetConnectTimeout(mqttConnectTimeout)
	opts.Dialer.Timeout = mqttConnectTimeout
	opts.SetCustomOpenConnectionFn(openLimitedMQTTConnection)
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
	}

	var connectionGeneration atomic.Uint64
	opts.OnConnect = func(client mqtt.Client) {
		slog.Info("connected to broker", "endpoint", endpoint)
		generation := connectionGeneration.Add(1)
		store.SetConnected(true)
		retrySYSSubscription(client, store, func() bool {
			return connectionGeneration.Load() == generation
		}, mqttSubscribeRetry)
	}
	opts.OnConnectionLost = func(_ mqtt.Client, err error) {
		connectionGeneration.Add(1)
		store.SetConnected(false)
		slog.Warn("connection to broker lost", "endpoint", endpoint, "error", err)
	}

	client := mqtt.NewClient(opts)

	// Keep handlers on a private mux instead of mutable package-global state.
	mux := http.NewServeMux()
	mux.Handle("/metrics", newMetricsHandler(registry))
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

	// Connect in the background so /metrics is already
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
	store.SetConnected(false)

	// Shutdown order: MQTT is already stopped above;
	// MeterProvider.Shutdown performs its own final collect/export per
	// reader before releasing resources (metric.PeriodicReader.Shutdown
	// flushes pending telemetry internally, so a separate ForceFlush call
	// would be redundant) - only then does HTTP stop serving, so a scrape
	// can observe the exporter's true final state before the socket closes.
	//
	// Given its own short, independent budget rather than sharing the HTTP
	// server's: an unreachable OTLP collector (opt-in, best-effort) must
	// only ever cost a small fixed delay on shutdown, never eat into or
	// block the HTTP server's own shutdown window. A failure here is logged,
	// not fatal - this is telemetry export, not the exporter's core function.
	otelShutdownCtx, otelCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := meterProvider.Shutdown(otelShutdownCtx); err != nil {
		slog.Warn("shutting down OTel meter provider", "error", err)
	}
	otelCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && runErr == nil {
		runErr = fmt.Errorf("shutting down HTTP server: %w", err)
	}

	return runErr
}
