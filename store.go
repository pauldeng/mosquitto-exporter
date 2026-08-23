package main

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// sample is one cached $SYS value: its last known reading, the Prometheus
// type it renders as, and when it was last updated. lastSeen is not read
// anywhere yet - it exists so the bounded-eviction cache from DESIGN.md
// §5.4 (a later phase) does not need a second migration to add it; Update
// already needs a clock read to get here.
type sample struct {
	value    float64
	kind     metricKind
	lastSeen time.Time
}

// Store is the single source of truth for every $SYS value the exporter has
// seen, and the single Prometheus Collector that renders them. Replacing
// the old per-topic dynamically-registered metrics (one prometheus.Gauge or
// *MosquittoCounter created and prometheus.MustRegister'd per newly-seen
// topic) with one mutex-guarded map plus one Collector fixes four defects
// in a single change - see DESIGN.md §7 Phase 1 item 1 for why they are not
// patched separately:
//
//   - C1: MosquittoCounter's hand-rolled counter.value was a plain float64
//     written by the MQTT goroutine and read by the scrape goroutine.
//   - C2: counterMetrics/gaugeMetrics were plain maps written by the MQTT
//     goroutine and ranged over by resetMetrics() on the reconnect
//     goroutine - a concurrent map read/write, which is a fatal crash, not
//     merely a race.
//   - C4: prometheus.MustRegister panicked the first time two different
//     $SYS topics produced the same metric name. Nothing is dynamically
//     registered anymore, so there is nothing left to collide.
//   - S5: every previously-unseen topic used to permanently register a new
//     Collector with the global registry. Now exactly one Collector (this
//     Store) is ever registered, regardless of how many distinct topics the
//     broker sends. The internal map can still grow with broker-controlled
//     topics - that bound is deliberately deferred to the eviction scheme
//     in DESIGN.md §5.4, not part of this fix.
//
// Store is an "unchecked" prometheus.Collector: Describe sends no
// descriptors, because the set of metrics it exports is determined at
// runtime by whatever topics the broker has sent. See the Collector
// interface doc on client_golang/prometheus for why partially describing a
// dynamic collector (some fixed Descs but not the per-topic ones) is not a
// supported option - it's all-or-nothing.
// maxTrackedTopics bounds the sample map against broker-controlled input.
// A real 2.1.2 broker publishes 56 $SYS topics, so this is ~18x headroom and
// no real deployment can reach it; it exists so a misbehaving or hostile
// publisher cannot grow the map without limit (S5's storage half).
//
// Deliberately a const, not a flag: nobody has a legitimate reason to tune
// it, and Phase 3's static topic table replaces this with an exact allowlist.
const maxTrackedTopics = 1024

type Store struct {
	mu              sync.RWMutex
	samples         map[string]sample
	connected       bool
	invalidPayloads uint64
	droppedTopics   uint64

	upDesc      *prometheus.Desc
	invalidDesc *prometheus.Desc
	droppedDesc *prometheus.Desc
}

// NewStore returns an empty, ready-to-use Store.
func NewStore() *Store {
	return &Store{
		samples: make(map[string]sample),
		upDesc: prometheus.NewDesc(
			"mosquitto_up",
			"Whether the exporter is currently connected to the Mosquitto broker (1) or not (0).",
			nil, nil,
		),
		invalidDesc: prometheus.NewDesc(
			"mosquitto_exporter_invalid_payloads_total",
			"Total number of $SYS payloads that could not be parsed as a number and were therefore not recorded.",
			nil, nil,
		),
		droppedDesc: prometheus.NewDesc(
			"mosquitto_exporter_dropped_topics_total",
			"Total number of $SYS topics not tracked because the sample map hit its bound.",
			nil, nil,
		),
	}
}

// Update records topic's value. Called from the MQTT handler goroutine.
//
// Ignored topics (deprecated aliases, static build-time strings) produce no
// sample, exactly as before. A payload that does not parse to a number also
// produces no sample - it is counted instead (C5: a parse failure must
// never look like a genuine zero reading).
func (s *Store) Update(topic, payload string) {
	kind, ok := classifyTopic(topic)
	if !ok {
		return
	}

	value, ok := parseValue(payload)
	if !ok {
		s.mu.Lock()
		s.invalidPayloads++
		s.mu.Unlock()
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, known := s.samples[topic]; !known && len(s.samples) >= maxTrackedTopics {
		// Never evict a topic we already track: $SYS is change-gated, so a
		// healthy-but-constant topic (publish_messages_dropped sitting at 0)
		// can legitimately go hours without a republish. Dropping it would
		// make the series vanish exactly when everything is fine.
		s.droppedTopics++
		return
	}
	s.samples[topic] = sample{value: value, kind: kind, lastSeen: time.Now()}
}

// SetConnected records the broker connection state. Called from the
// lifecycle goroutine; drives the mosquitto_up gauge.
func (s *Store) SetConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}

// Reset zeroes every cached value without forgetting which topics have been
// seen, preserving today's --reset-metrics behaviour. That flag is a
// breaking change to remove (it fabricates counter resets on every broker
// flap - DESIGN.md §5.5) and belongs in the breaking release, not Phase 1.
func (s *Store) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for topic, sm := range s.samples {
		sm.value = 0
		s.samples[topic] = sm
	}
}

// Describe intentionally sends nothing: see the Store doc comment above.
func (s *Store) Describe(_ chan<- *prometheus.Desc) {}

// Collect renders mosquitto_up, the invalid-payload counter, and every
// cached $SYS value as const metrics.
func (s *Store) Collect(ch chan<- prometheus.Metric) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	up := 0.0
	if s.connected {
		up = 1.0
	}
	ch <- prometheus.MustNewConstMetric(s.upDesc, prometheus.GaugeValue, up)
	ch <- prometheus.MustNewConstMetric(s.invalidDesc, prometheus.CounterValue, float64(s.invalidPayloads))
	ch <- prometheus.MustNewConstMetric(s.droppedDesc, prometheus.CounterValue, float64(s.droppedTopics))

	for topic, sm := range s.samples {
		valueType := prometheus.GaugeValue
		if sm.kind == kindCounter {
			valueType = prometheus.CounterValue
		}
		desc := prometheus.NewDesc(parseTopic(topic), topic, nil, nil)
		ch <- prometheus.MustNewConstMetric(desc, valueType, sm.value)
	}
}
