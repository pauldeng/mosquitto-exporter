package exporter

// Phase 1 lifecycle/TLS tests (DESIGN.md §7 Phase 1, §8 gates G4/G5). These
// cover the pieces AGENT-2 owns in main.go: serve-before-connect (R1), TLS
// hardening (S1/S2), credential handling (S3/decision 10.14), and the
// explicit http.Server timeouts (S4/S6).

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

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

	t.Run("insecure-skip-verify defaults to false and MinVersion is TLS 1.2 (S1)", func(t *testing.T) {
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

	t.Run("missing keypair fails closed (S2)", func(t *testing.T) {
		t.Parallel()
		_, err := buildTLSConfig("/nonexistent/cert.pem", "/nonexistent/key.pem", "", false)
		if err == nil {
			t.Fatal("expected an error for a missing keypair; today's bug logs and continues (fails open)")
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

// TestTLSHandshake_VerifiesByDefault proves G4's "the client refuses a
// broker presenting an untrusted certificate unless insecure mode is
// explicitly opted into" with a real TLS handshake, not just field
// inspection.
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
		t.Error("ReadHeaderTimeout must be non-zero (S4 / gosec G114)")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout must be non-zero (S4 / gosec G114)")
	}
	if srv.WriteTimeout <= 0 {
		t.Error("WriteTimeout must be non-zero (S4 / gosec G114)")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout must be non-zero (S4 / gosec G114)")
	}
}

// TestMetricsServesMosquittoUpZeroWithoutBroker is G5's single most
// important gate: "/metrics returns 200 with mosquitto_up 0 while the
// broker is down." It exercises the same wiring runServer does - a Store
// registered into the default Prometheus registry, reporting disconnected -
// without needing a live broker or HTTP listener.
//
// See also the live end-to-end proof in the Phase 1 handoff report: the
// built binary, pointed at a closed port, answers `curl /metrics` with
// `mosquitto_up 0` at HTTP 200 while it keeps retrying the broker in the
// background.
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

//nolint:paralleltest // starts the process-level signal handler used by runServer
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
			t.Fatalf("runServer returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runServer did not stop within 5 seconds")
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("shutdown took %s, want under 5s", elapsed)
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
