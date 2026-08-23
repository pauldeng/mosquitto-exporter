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
	"maps"
	"sync"

	"github.com/pauldeng/mosquitto-exporter/internal/sys"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// brokerMetrics owns every mosquitto_broker_* OTel instrument. Instruments
// are declared lazily - the first time Store resolves a given metric name -
// because the static topic->name table lives in internal/sys and is not
// exposed for enumeration (that package owns all topic knowledge; this
// package only ever learns a name the moment sys.Resolve first produces it,
// via Store.Update's return value - see server.go's message handler).
//
// Observable, not synchronous (DESIGN.md §5.3): only observable instruments
// can decline to report a value for a collection cycle, which is what lets
// a disconnected or heartbeat-stalled broker make every mosquitto_broker_*
// series vanish together instead of freezing at a stale value forever.
type brokerMetrics struct {
	meter metric.Meter
	store *Store

	mu       sync.Mutex // guards declaring instruments and (re)registering the callback below; never held while that callback runs
	counters map[string]metric.Float64ObservableCounter
	gauges   map[string]metric.Float64ObservableGauge
	reg      metric.Registration
}

func newBrokerMetrics(meter metric.Meter, store *Store) *brokerMetrics {
	return &brokerMetrics{
		meter:    meter,
		store:    store,
		counters: make(map[string]metric.Float64ObservableCounter),
		gauges:   make(map[string]metric.Float64ObservableGauge),
	}
}

// Ensure declares an OTel instrument for sample.Name the first time it is
// seen, then rebuilds the single shared callback registration to cover it.
// A no-op (a map lookup under mu) for every later sample sharing that name -
// the common case, since a real broker's set of names stabilises after its
// first $SYS tick (DESIGN.md §7 Phase 3 finding).
//
// Called synchronously right after Store.Update resolves a sample - never
// from inside the observe callback itself, so RegisterCallback is never
// invoked re-entrantly.
func (b *brokerMetrics) Ensure(sample sys.Sample) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.counters[sample.Name]; ok {
		return nil
	}
	if _, ok := b.gauges[sample.Name]; ok {
		return nil
	}

	// Copy-on-write: the callback registered below closes over these two
	// maps by value (the map header), never over b.counters/b.gauges
	// directly, so observe() never needs to lock b.mu itself - the OTel
	// Callback doc explicitly warns against locking mutexes inside a
	// callback.
	counters := maps.Clone(b.counters)
	gauges := maps.Clone(b.gauges)

	switch sample.Kind {
	case sys.Counter:
		inst, err := b.meter.Float64ObservableCounter(sample.Name, metric.WithDescription(sample.Help))
		if err != nil {
			return fmt.Errorf("declaring OTel counter %s: %w", sample.Name, err)
		}
		counters[sample.Name] = inst
	case sys.Gauge:
		inst, err := b.meter.Float64ObservableGauge(sample.Name, metric.WithDescription(sample.Help))
		if err != nil {
			return fmt.Errorf("declaring OTel gauge %s: %w", sample.Name, err)
		}
		gauges[sample.Name] = inst
	default:
		return fmt.Errorf("unrecognised sys.Kind %d for metric %s", sample.Kind, sample.Name)
	}

	insts := make([]metric.Observable, 0, len(counters)+len(gauges))
	for _, c := range counters {
		insts = append(insts, c)
	}
	for _, g := range gauges {
		insts = append(insts, g)
	}

	if b.reg != nil {
		if err := b.reg.Unregister(); err != nil {
			return fmt.Errorf("unregistering previous OTel callback: %w", err)
		}
	}
	reg, err := b.meter.RegisterCallback(observeFunc(b.store, counters, gauges), insts...)
	if err != nil {
		return fmt.Errorf("registering OTel callback: %w", err)
	}

	b.counters, b.gauges, b.reg = counters, gauges, reg
	return nil
}

// observeFunc closes over immutable snapshots of the instrument maps taken
// at registration time, so the returned Callback needs no locking of its
// own: it only ever reads store's cache (which has its own lock) and two
// maps nothing else can mutate.
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
