package exporter

import (
	"context"
	"testing"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/config"
)

// TestNewOTLPExporter covers every branch of newOTLPExporter's protocol/
// scheme dispatch (DESIGN.md §7 Phase 4 item 5). None of these dial
// synchronously (grpc and http OTLP exporters connect lazily), so
// constructing against an address nothing listens on is safe and fast; each
// case still Shuts down its exporter so no client-internal goroutine leaks
// past the test (goleak.VerifyTestMain in TestMain would otherwise catch it).
func TestNewOTLPExporter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cfg     config.Config
		wantErr bool
	}{
		{
			name: "grpc without scheme uses WithEndpoint",
			cfg:  config.Config{OTLPProtocol: "grpc", OTLPEndpoint: "127.0.0.1:4317"},
		},
		{
			name: "grpc with scheme uses WithEndpointURL",
			cfg:  config.Config{OTLPProtocol: "grpc", OTLPEndpoint: "http://127.0.0.1:4317"},
		},
		{
			name: "http without scheme uses WithEndpoint",
			cfg:  config.Config{OTLPProtocol: "http", OTLPEndpoint: "127.0.0.1:4318"},
		},
		{
			name: "http with scheme uses WithEndpointURL",
			cfg:  config.Config{OTLPProtocol: "http", OTLPEndpoint: "http://127.0.0.1:4318"},
		},
		{
			name:    "unsupported protocol is rejected",
			cfg:     config.Config{OTLPProtocol: "carrier-pigeon", OTLPEndpoint: "127.0.0.1:1"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(t.Context(), otlpDialTimeout)
			defer cancel()

			exp, err := newOTLPExporter(ctx, tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("newOTLPExporter: %v", err)
			}
			t.Cleanup(func() {
				shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer shutdownCancel()
				if err := exp.Shutdown(shutdownCtx); err != nil {
					t.Errorf("Shutdown: %v", err)
				}
			})
		})
	}
}

// TestNewOTLPReader proves newOTLPReader wraps a working exporter in a
// PeriodicReader at the configured interval, and that the whole thing shuts
// down cleanly (no leaked goroutines) - the same shutdown path server.go's
// Run relies on via MeterProvider.Shutdown.
func TestNewOTLPReader(t *testing.T) {
	t.Parallel()
	reader, err := newOTLPReader(config.Config{
		OTLPProtocol: "grpc",
		OTLPEndpoint: "127.0.0.1:4317",
		OTLPInterval: time.Minute,
	})
	if err != nil {
		t.Fatalf("newOTLPReader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := reader.Shutdown(ctx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}
