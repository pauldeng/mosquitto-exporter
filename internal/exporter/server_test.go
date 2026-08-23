package exporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"
)

type blockingGatherer struct {
	entered chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (g *blockingGatherer) Gather() ([]*dto.MetricFamily, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return nil, nil
}

//nolint:paralleltest // temporarily constrains GOMAXPROCS to schedule each blocked request deterministically
func TestMetricsHandlerBoundsConcurrentScrapes(t *testing.T) {
	oldProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(oldProcs) })
	release := make(chan struct{})
	gatherer := &blockingGatherer{entered: make(chan struct{}), release: release}
	handler := newMetricsHandler(gatherer)
	done := make(chan struct{}, maxMetricsRequests)

	for range maxMetricsRequests {
		go func() {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/metrics", nil))
			done <- struct{}{}
		}()
		runtime.Gosched()
	}
	<-gatherer.entered

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusServiceUnavailable {
		close(release)
		t.Fatalf("overflow scrape status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	close(release)
	for range maxMetricsRequests {
		<-done
	}
}

type testToken struct {
	done bool
	err  error
}

func (t testToken) Wait() bool                     { return t.done }
func (t testToken) WaitTimeout(time.Duration) bool { return t.done }
func (t testToken) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (t testToken) Error() error { return t.err }

type testSubscriber struct {
	tokens    []mqtt.Token
	calls     int
	connected bool
}

func (s *testSubscriber) IsConnected() bool { return s.connected }
func (s *testSubscriber) Subscribe(_ string, _ byte, _ mqtt.MessageHandler) mqtt.Token {
	token := s.tokens[s.calls]
	s.calls++
	return token
}

func TestRetrySYSSubscription(t *testing.T) {
	t.Parallel()
	subscriber := &testSubscriber{
		connected: true,
		tokens: []mqtt.Token{
			testToken{done: true, err: errors.New("suback rejected")},
			testToken{done: true},
		},
	}
	retrySYSSubscription(subscriber, NewStore(time.Second), func() bool { return true }, 0)
	if subscriber.calls != 2 {
		t.Fatalf("Subscribe calls = %d, want 2", subscriber.calls)
	}
}

func TestMQTTPacketLimitConn(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	limited := &mqttPacketLimitConn{Conn: client}

	go func() {
		_, _ = server.Write([]byte{0x30, 0x81, 0x80, 0x04}) // Remaining Length = 65,537.
	}()
	one := make([]byte, 1)
	for range 3 {
		if _, err := io.ReadFull(limited, one); err != nil {
			t.Fatalf("reading header byte: %v", err)
		}
	}
	if _, err := io.ReadFull(limited, one); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized Remaining Length error = %v", err)
	}
}

func TestMQTTPacketLimitConnAcceptsBoundedPackets(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() { _ = server.Close(); _ = client.Close() })
	limited := &mqttPacketLimitConn{Conn: client}
	want := []byte{0x30, 0x03, '1', '2', '3', 0xd0, 0x00}
	go func() { _, _ = server.Write(want) }()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(limited, got); err != nil {
		t.Fatalf("reading bounded packets: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("bounded packets changed: got %v, want %v", got, want)
	}
}

// writeTempCA writes a throwaway self-signed CA certificate (PEM) to a file
// under dir and returns its path. Used only to exercise buildTLSConfig's
// --ca-cert loading path with syntactically valid PEM content; it is never
// meant to match any real server's certificate.
func writeTempCA(t *testing.T, dir string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating throwaway CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating throwaway CA cert: %v", err)
	}
	path := filepath.Join(dir, "ca.pem")
	f, err := os.Create(path) //nolint:gosec // G304: path is built from t.TempDir(), not external input
	if err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	if err := pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("encoding PEM: %v", err)
	}
	return path
}

func TestBuildTLSConfig(t *testing.T) {
	t.Run("no TLS flags at all returns nil, nil", func(t *testing.T) {
		t.Parallel()
		cfg, err := buildTLSConfig("", "", "", false)
		if err != nil || cfg != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", cfg, err)
		}
	})

	t.Run("insecure-skip-verify defaults to false and MinVersion is TLS 1.2", func(t *testing.T) {
		t.Parallel()
		caFile := writeTempCA(t, t.TempDir())
		cfg, err := buildTLSConfig("", "", caFile, false)
		if err != nil {
			t.Fatalf("buildTLSConfig: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected a non-nil TLS config when --ca-cert is set")
		}
		if cfg.InsecureSkipVerify {
			t.Error("InsecureSkipVerify must default to false")
		}
		if cfg.MinVersion != tls.VersionTLS12 {
			t.Errorf("MinVersion = %v, want tls.VersionTLS12", cfg.MinVersion)
		}
	})

	t.Run("insecure-skip-verify=true is honoured", func(t *testing.T) {
		t.Parallel()
		cfg, err := buildTLSConfig("", "", "", true)
		if err != nil {
			t.Fatalf("buildTLSConfig: %v", err)
		}
		if cfg == nil || !cfg.InsecureSkipVerify {
			t.Errorf("got %+v, want a config with InsecureSkipVerify=true", cfg)
		}
	})

	t.Run("missing keypair fails closed", func(t *testing.T) {
		t.Parallel()
		_, err := buildTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
		if err == nil {
			t.Fatal("expected an error for a missing keypair")
		}
	})

	t.Run("only --cert without --key is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := buildTLSConfig("/some/cert.pem", "", "", false)
		if err == nil {
			t.Fatal("expected an error when only --cert is set")
		}
	})

	t.Run("missing --ca-cert file returns an error", func(t *testing.T) {
		t.Parallel()
		_, err := buildTLSConfig("", "", "/nonexistent/ca.pem", false)
		if err == nil {
			t.Fatal("expected an error for a missing --ca-cert file")
		}
	})

	t.Run("--ca-cert with invalid PEM content returns an error", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		badFile := filepath.Join(dir, "bad-ca.pem")
		if err := os.WriteFile(badFile, []byte("not a certificate"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := buildTLSConfig("", "", badFile, false)
		if err == nil {
			t.Fatal("expected an error for invalid PEM content")
		}
	})
}

// TestTLSHandshake_VerifiesByDefault uses a real TLS handshake to verify that
// untrusted certificates are rejected unless insecure mode is explicitly set.
func TestTLSHandshake_VerifiesByDefault(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "https://")

	t.Run("nil config (paho's default when no TLS flags are set) rejects the self-signed cert", func(t *testing.T) {
		t.Parallel()
		conn, err := tls.Dial("tcp", addr, nil)
		if err == nil {
			_ = conn.Close()
			t.Fatal("expected TLS dial without --insecure-skip-verify to fail against a self-signed certificate")
		}
	})

	t.Run("--insecure-skip-verify accepts the same self-signed cert", func(t *testing.T) {
		t.Parallel()
		cfg, err := buildTLSConfig("", "", "", true)
		if err != nil {
			t.Fatalf("buildTLSConfig: %v", err)
		}
		conn, err := tls.Dial("tcp", addr, cfg)
		if err != nil {
			t.Fatalf("expected dial to succeed with --insecure-skip-verify: %v", err)
		}
		_ = conn.Close()
	})
}

func TestNewHTTPServer_AllTimeoutsSet(t *testing.T) {
	t.Parallel()
	srv := newHTTPServer("127.0.0.1:0", http.NewServeMux())
	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout must be non-zero (gosec G114)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be non-zero (gosec G114)")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be non-zero (gosec G114)")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be non-zero (gosec G114)")
	}
}

// TestMetricsServesMosquittoUpZeroWithoutBroker verifies that a disconnected
// store serves mosquitto_up 0 without requiring a broker or HTTP listener.
func TestMetricsServesMosquittoUpZeroWithoutBroker(t *testing.T) {
	t.Parallel()
	// Own registry, not the default one: main_test.go registers sharedStore
	// into the default registry at init, so a second Store there would emit
	// mosquitto_up twice and Gather would fail the scrape with a 500.
	store := NewStore(10 * time.Second)
	reg := prometheus.NewRegistry()
	reg.MustRegister(store)

	store.SetConnected(false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "mosquitto_up 0") {
		t.Errorf("/metrics does not contain %q; body:\n%s", "mosquitto_up 0", body)
	}
}

//nolint:paralleltest // starts the process-level signal handler used by Run
func TestRunServerServesWhileBrokerIsDownAndShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, config.Config{
			Endpoint:    "tcp://127.0.0.1:1",
			BindAddress: addr,
			Version:     "test",
			SysInterval: 10 * time.Second,
		})
	}()

	metricsURL := "http://" + addr + "/metrics"
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, getErr := http.Get(metricsURL) //nolint:gosec // fixed local test URL
		if getErr == nil {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr == nil && resp.StatusCode == http.StatusOK && strings.Contains(string(body), "mosquitto_up 0") {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("/metrics did not serve mosquitto_up 0 while broker was down")
		}
		time.Sleep(25 * time.Millisecond)
	}

	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop within 5 seconds")
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("shutdown took %s, want under 5s", elapsed)
	}
}

// TestRunServerWithOTLPEndpointConfigured proves the opt-in OTLP push reader
// wires into Run without blocking startup or
// shutdown, even though nothing is listening at the configured endpoint: the
// gRPC OTLP exporter dials lazily, so /metrics must still serve promptly and
// the whole process must still stop within the same budget as the
// OTLP-disabled case above.
//
//nolint:paralleltest // starts the process-level signal handler used by Run
func TestRunServerWithOTLPEndpointConfigured(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, config.Config{
			Endpoint:     "tcp://127.0.0.1:1",
			BindAddress:  addr,
			Version:      "test",
			SysInterval:  10 * time.Second,
			OTLPEndpoint: "127.0.0.1:1", // nothing listens here; must never block
			OTLPProtocol: "grpc",
			OTLPInterval: time.Minute,
		})
	}()

	metricsURL := "http://" + addr + "/metrics"
	deadline := time.Now().Add(3 * time.Second)
	for {
		resp, getErr := http.Get(metricsURL) //nolint:gosec // fixed local test URL
		if getErr == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("/metrics did not serve while the OTLP endpoint was unreachable")
		}
		time.Sleep(25 * time.Millisecond)
	}

	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("Run did not stop within 8 seconds")
	}
	// The OTel MeterProvider shutdown against an unreachable OTLP endpoint is
	// bounded to its own short budget (server.go's otelShutdownCtx) rather
	// than blocking indefinitely, so total shutdown must still stay well
	// under the previous HTTP-only budget's headroom.
	if elapsed := time.Since(started); elapsed >= 8*time.Second {
		t.Fatalf("shutdown took %s, want under 8s", elapsed)
	}
}

func TestServeVersion(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	serveVersion("test")(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Mosquitto exporter") {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}
