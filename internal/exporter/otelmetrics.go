package exporter

// otelmetrics.go declares the mosquitto_broker_* OTel instrument set against
// the stable v1 go.opentelemetry.io/otel/metric API only (DESIGN.md §5.2):
// no symbol from the experimental go.opentelemetry.io/otel/exporters/prometheus
// module may appear in this file - that containment is confined to
// otelprom.go instead, so a breaking change in the experimental module is a
// compile error there, not here.

import (
	"context"
	"fmt"

	"github.com/pauldeng/mosquitto-exporter/internal/sys"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Observable, not synchronous (DESIGN.md §5.3): only observable instruments
// can decline to report a value for a collection cycle, which is what lets
// a disconnected or heartbeat-stalled broker make every mosquitto_broker_*
// series vanish together instead of freezing at a stale value forever.
func registerBrokerMetrics(meter metric.Meter, store *Store) error {
	specs := sys.Instruments()
	counters := make(map[string]metric.Float64ObservableCounter)
	gauges := make(map[string]metric.Float64ObservableGauge)
	insts := make([]metric.Observable, 0, len(specs))
	for _, spec := range specs {
		switch spec.Kind {
		case sys.Counter:
			inst, err := meter.Float64ObservableCounter(spec.Name, metric.WithDescription(spec.Help), metric.WithUnit(spec.Unit))
			if err != nil {
				return fmt.Errorf("declaring OTel counter %s: %w", spec.Name, err)
			}
			counters[spec.Name] = inst
			insts = append(insts, inst)
		case sys.Gauge:
			inst, err := meter.Float64ObservableGauge(spec.Name, metric.WithDescription(spec.Help), metric.WithUnit(spec.Unit))
			if err != nil {
				return fmt.Errorf("declaring OTel gauge %s: %w", spec.Name, err)
			}
			gauges[spec.Name] = inst
			insts = append(insts, inst)
		default:
			return fmt.Errorf("unrecognised sys.Kind %d for metric %s", spec.Kind, spec.Name)
		}
	}
	_, err := meter.RegisterCallback(observeFunc(store, counters, gauges), insts...)
	if err != nil {
		return fmt.Errorf("registering OTel callback: %w", err)
	}
	return nil
}

func observeFunc(
	store *Store,
	counters map[string]metric.Float64ObservableCounter,
	gauges map[string]metric.Float64ObservableGauge,
) metric.Callback {
	return func(_ context.Context, o metric.Observer) error {
		up, samples := store.BrokerSnapshot()
		if !up {
			// DESIGN.md §5.3: disconnected, or the uptime heartbeat has
			// stalled - observe nothing so every mosquitto_broker_* series
			// goes absent together, rather than freezing at a stale value.
			return nil
		}
		for _, s := range samples {
			opt := metric.WithAttributes(attributesFromLabels(s.Labels)...)
			if inst, ok := counters[s.Name]; ok {
				o.ObserveFloat64(inst, s.Value, opt)
				continue
			}
			if inst, ok := gauges[s.Name]; ok {
				o.ObserveFloat64(inst, s.Value, opt)
			}
		}
		return nil
	}
}

func attributesFromLabels(labels map[string]string) []attribute.KeyValue {
	if len(labels) == 0 {
		return nil
	}
	attrs := make([]attribute.KeyValue, 0, len(labels))
	for k, v := range labels {
		attrs = append(attrs, attribute.String(k, v))
	}
	return attrs
}
