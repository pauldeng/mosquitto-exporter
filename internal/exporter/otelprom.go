package exporter

// otelprom.go is the ONLY file in this package allowed to import
// go.opentelemetry.io/otel/exporters/prometheus (v0.67.0, OTel's
// experimental-metrics module set - not covered by OTel's "metrics are
// stable" guarantee) or github.com/prometheus/otlptranslator, its
// translation-strategy type. Every instrument is declared against the
// stable v1 otel/metric API in otelmetrics.go instead, so a breaking change
// in this experimental module is a contained compile error in this one
// file, not a diffuse refactor (DESIGN.md §5.2/§9.4).
//
// UnderscoreEscapingWithoutSuffixes is pinned explicitly rather than left at
// the package default: the exporter's own WithTranslationStrategy doc says
// that default is planned to change in a future release, and our instrument
// names (otelmetrics.go) are already the final Prometheus names complete
// with their _total/_seconds/_bytes suffixes (DESIGN.md §6) - any
// suffix-adding strategy would double them into
// e.g. mosquitto_broker_bytes_received_total_total. WithoutTargetInfo and
// WithoutScopeInfo are likewise required: without them target_info and
// otel_scope_* series appear and break the golden /metrics fixture.

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/otlptranslator"
	otelprometheus "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

// newPrometheusReader builds the pull-based OTel reader behind /metrics
// (DESIGN.md §5.2's diagram), registering itself into registry alongside
// Store and client_golang's process/Go collectors (§6.4).
func newPrometheusReader(registry *prometheus.Registry) (sdkmetric.Reader, error) {
	reader, err := otelprometheus.New(
		otelprometheus.WithRegisterer(registry),
		otelprometheus.WithTranslationStrategy(otlptranslator.UnderscoreEscapingWithoutSuffixes),
		otelprometheus.WithoutTargetInfo(),
		otelprometheus.WithoutScopeInfo(),
	)
	if err != nil {
		return nil, fmt.Errorf("building OTel Prometheus reader: %w", err)
	}
	return reader, nil
}
