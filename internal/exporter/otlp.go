package exporter

// otlp.go wires an opt-in OTLP push reader into the same MeterProvider as
// the Prometheus reader. It is off by default and remains experimental until
// broker counter resets can be represented with a correct StartTime.
//
// $SYS counters reset to zero on every broker restart.
// Classic Prometheus exposition carries no StartTime, so a decrease just
// looks like an ordinary counter reset and rate() handles it exactly as it
// does today. OTLP cumulative Sums are spec-invalid under that same reset
// (StartTime never advances), and a StartTime-aware backend may drop or
// mis-render the point. This is a known limitation - see
// sdk/metric/internal/aggregate/sum.go's precomputedSum, which performs no
// monotonicity or reset validation. See docs/OTEL.md.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// otlpDialTimeout bounds building the OTLP exporter at startup so an
// unreachable collector cannot delay /metrics indefinitely.
const otlpDialTimeout = 5 * time.Second

// newOTLPReader builds the opt-in PeriodicReader push path from cfg. Callers
// must only invoke this when cfg.OTLPEndpoint != "" (config.Config.Validate
// enforces OTLPProtocol/OTLPInterval are sane whenever it is set).
func newOTLPReader(cfg config.Config) (sdkmetric.Reader, error) {
	ctx, cancel := context.WithTimeout(context.Background(), otlpDialTimeout)
	defer cancel()

	exporter, err := newOTLPExporter(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("building OTLP metric exporter: %w", err)
	}
	return sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(cfg.OTLPInterval)), nil
}

// newOTLPExporter picks the grpc or http exporter per cfg.OTLPProtocol.
// cfg.OTLPEndpoint is accepted either as a bare host:port (the documented
// grpc form, e.g. "localhost:4317") or a full scheme://host:port URL (the
// documented http form, e.g. "http://localhost:4318") - detected by the
// presence of "://" rather than forcing one form per protocol, since both
// exporters accept either.
func newOTLPExporter(ctx context.Context, cfg config.Config) (sdkmetric.Exporter, error) {
	hasScheme := strings.Contains(cfg.OTLPEndpoint, "://")

	switch cfg.OTLPProtocol {
	case "grpc":
		if hasScheme {
			return otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpointURL(cfg.OTLPEndpoint))
		}
		return otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithEndpoint(cfg.OTLPEndpoint))
	case "http":
		if hasScheme {
			return otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.OTLPEndpoint))
		}
		return otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpoint(cfg.OTLPEndpoint))
	default:
		// config.Config.Validate already rejects this before Run ever calls
		// here; kept as a defensive boundary, not a reachable production path.
		return nil, fmt.Errorf("unsupported otlp-protocol %q", cfg.OTLPProtocol)
	}
}
