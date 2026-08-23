package exporter

import (
	"sync"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/sys"
	"github.com/prometheus/client_golang/prometheus"
)

// maxTrackedTopics bounds the sample map against broker-controlled input.
// internal/sys's static table has ~30-odd exportable metric families
// (DESIGN.md §5.4/§6), so this is well over 30x headroom and no real
// deployment can reach it; it exists as a backstop against a table bug or a
// future broker version adding topics, not as a limit anyone should tune.
//
// Deliberately a const, not a flag: nobody has a legitimate reason to tune
// it.
const maxTrackedTopics = 1024

// uptimeTopic is the one $SYS topic Mosquitto publishes unconditionally
// every tick, never change-gated (DESIGN.md §4, §5.3). It is therefore the
// liveness heartbeat: Store treats its own staleness, not any other topic's,
// as proof the broker's $SYS tree has stalled.
const uptimeTopic = "$SYS/broker/uptime"

// heartbeatMultiplier is how many sys-intervals of uptime silence, while
// still connected, before Store reports mosquitto_up 0 (DESIGN.md §5.3).
const heartbeatMultiplier = 3

// cachedSample is one resolved, exportable $SYS value: the resolved sample
// itself (name/kind/help/labels/value), plus the Desc built for it once at
// cache time - kept only to detect two topics aliasing the same underlying
// series (see the dedup scan in Update), never rendered directly since
// Phase 4 (DESIGN.md §7): mosquitto_broker_* series are now rendered
// exclusively through the OTel instruments in otelmetrics.go, which read the
// cache via BrokerSnapshot.
type cachedSample struct {
	sys.Sample
	desc *prometheus.Desc
}

// Store is the single source of truth for every exportable $SYS value the
// exporter has seen. It is normally keyed on the raw $SYS topic. Alias
// topics resolving to the same name and labels update the first cached
// entry instead of creating a duplicate series.
//
// Every resolved sample comes from sys.Resolve, which is also what decides
// whether a topic is exported at all (DESIGN.md §5.4's static table replaces
// the old "any $SYS topic auto-registers a metric" behaviour):
//
//   - sys.OK: the value is cached (or updates an existing cache entry).
//   - sys.Excluded: recognised but deliberately not exported (the 24 dropped
//     load/** series); silently ignored, deliberately not counted.
//   - sys.NotExported: the topic is not in the static table; counted by
//     mosquitto_exporter_unknown_topics_total, never cached per-topic, so an
//     attacker flooding unknown $SYS topics cannot grow memory.
//   - sys.InvalidPayload: the topic is known but the payload did not parse
//     to a number; counted by mosquitto_exporter_invalid_payloads_total (C5:
//     a parse failure must never look like a genuine zero reading).
//
// Store is also an "unchecked" prometheus.Collector for its own self-metrics
// only (mosquitto_up and the three mosquitto_exporter_* counters below):
// Describe sends no descriptors, matching how client_golang expects a
// dynamic collector to identify itself. mosquitto_broker_* series are not
// part of that Collector output; see the cachedSample and BrokerSnapshot
// docs above and below.
type Store struct {
	mu              sync.RWMutex
	samples         map[string]cachedSample
	connected       bool
	lastUptime      time.Time // last time uptimeTopic was resolved OK; zero if never
	sysInterval     time.Duration
	invalidPayloads uint64
	unknownTopics   uint64
	droppedTopics   uint64

	upDesc      *prometheus.Desc
	invalidDesc *prometheus.Desc
	unknownDesc *prometheus.Desc
	droppedDesc *prometheus.Desc
}

// NewStore returns an empty, ready-to-use Store. sysInterval sizes the
// uptime heartbeat window only (DESIGN.md §5.3) - it must be positive;
// config.Config.Validate enforces that before this is called.
func NewStore(sysInterval time.Duration) *Store {
	return &Store{
		samples:     make(map[string]cachedSample),
		sysInterval: sysInterval,
		upDesc: prometheus.NewDesc(
			"mosquitto_up",
			"Whether the exporter is currently connected to the Mosquitto broker and its $SYS tree is fresh (1) or not (0).",
			nil, nil,
		),
		invalidDesc: prometheus.NewDesc(
			"mosquitto_exporter_invalid_payloads_total",
			"Total number of $SYS payloads that could not be parsed as a number and were therefore not recorded.",
			nil, nil,
		),
		unknownDesc: prometheus.NewDesc(
			"mosquitto_exporter_unknown_topics_total",
			"Total number of $SYS publishes received on topics that are not in the exporter's static export table and were therefore not recorded.",
			nil, nil,
		),
		droppedDesc: prometheus.NewDesc(
			"mosquitto_exporter_dropped_topics_total",
			"Total number of new, otherwise-exportable $SYS topics dropped because the tracked-topic cache was full.",
			nil, nil,
		),
	}
}

// Update resolves topic/payload through sys.Resolve and records the result.
// Called from the MQTT handler goroutine.
func (s *Store) Update(topic, payload string) {
	sample, reason := sys.Resolve(topic, payload)

	s.mu.Lock()
	defer s.mu.Unlock()

	switch reason {
	case sys.Excluded:
		// Recognised and deliberately dropped (§6.3). Not counted: it is not
		// a surprise, and counting it would make the unknown-topic counter
		// rise forever on a healthy broker.
		return
	case sys.NotExported:
		s.unknownTopics++
		return
	case sys.InvalidPayload:
		s.invalidPayloads++
		return
	}

	if topic == uptimeTopic && s.connected {
		s.lastUptime = time.Now()
	}

	if cached, known := s.samples[topic]; known && sample.Name != "mosquitto_broker_info" {
		cached.Value = sample.Value
		s.samples[topic] = cached
		return
	}

	valueType := prometheus.GaugeValue
	if sample.Kind == sys.Counter {
		valueType = prometheus.CounterValue
	}
	desc := prometheus.NewDesc(sample.Name, sample.Help, nil, sample.Labels)
	if _, err := prometheus.NewConstMetric(desc, valueType, sample.Value); err != nil {
		s.droppedTopics++
		return
	}
	next := cachedSample{Sample: sample, desc: desc}
	if _, known := s.samples[topic]; known {
		// Replace the whole sample, not just its value: info-metric labels can
		// change after a broker upgrade.
		s.samples[topic] = next
		return
	}

	// Alias topics share a descriptor. Keep one cache entry and let the most
	// recent publish win, avoiding duplicate or map-order-dependent samples.
	// ponytail: this linear scan is capped at 1,024 entries; add a descriptor
	// index only if profiles show alias updates matter.
	for key, cached := range s.samples {
		if cached.desc.String() == desc.String() {
			s.samples[key] = next
			return
		}
	}

	if len(s.samples) >= maxTrackedTopics {
		// Never evict a topic we already track: $SYS is change-gated, so a
		// healthy-but-constant topic (publish_messages_dropped sitting at 0)
		// can legitimately go hours without a republish. Dropping it would
		// make the series vanish exactly when everything is fine.
		s.droppedTopics++
		return
	}
	s.samples[topic] = next
}

// SetConnected records the broker connection state. Called from the
// lifecycle goroutine; drives the mosquitto_up gauge together with the
// uptime heartbeat.
func (s *Store) SetConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	if !v {
		// A reconnect must prove liveness with a new uptime publish; otherwise
		// a recent heartbeat from the previous session can expose stale data.
		s.lastUptime = time.Time{}
	}
	s.mu.Unlock()
}

// up reports whether the exporter should claim liveness: connected, and the
// $SYS tree has produced an uptime tick within the heartbeat window
// (DESIGN.md §5.3). Callers must hold at least a read lock.
func (s *Store) up() bool {
	if !s.connected {
		return false
	}
	if s.lastUptime.IsZero() {
		return false
	}
	return time.Since(s.lastUptime) < heartbeatMultiplier*s.sysInterval
}

// Describe intentionally sends nothing: see the Store doc comment above.
func (s *Store) Describe(_ chan<- *prometheus.Desc) {}

// Collect renders mosquitto_up and the exporter's own self-metric counters.
// Since Phase 4 (DESIGN.md §7), mosquitto_broker_* series are rendered by
// the OTel instruments in otelmetrics.go instead - see BrokerSnapshot -
// which is what actually implements the §5.3/§5.5 "omit every broker series
// together while down" behaviour today; Store.Collect itself no longer
// touches s.samples.
func (s *Store) Collect(ch chan<- prometheus.Metric) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	up := 0.0
	if s.up() {
		up = 1.0
	}
	ch <- prometheus.MustNewConstMetric(s.upDesc, prometheus.GaugeValue, up)
	ch <- prometheus.MustNewConstMetric(s.invalidDesc, prometheus.CounterValue, float64(s.invalidPayloads))
	ch <- prometheus.MustNewConstMetric(s.unknownDesc, prometheus.CounterValue, float64(s.unknownTopics))
	ch <- prometheus.MustNewConstMetric(s.droppedDesc, prometheus.CounterValue, float64(s.droppedTopics))
}

// BrokerSnapshot is the OTel instrument set's only view into Store
// (otelmetrics.go's callback): up reports the same
// connection/heartbeat gate Collect uses for mosquitto_up, and samples is
// every cached $SYS value at the instant up was computed - both read under
// one lock so they describe the same instant (mirrors Collect's own
// up-then-samples read; §5.8's accepted torn-read trade-off still applies
// across the samples themselves).
//
// When up is false, samples is nil: the caller must omit every
// mosquitto_broker_* series together rather than report them flat or
// zeroed (DESIGN.md §5.3/§5.5) - the absence itself is the honest signal.
func (s *Store) BrokerSnapshot() (up bool, samples []sys.Sample) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.up() {
		return false, nil
	}
	samples = make([]sys.Sample, 0, len(s.samples))
	for _, sm := range s.samples {
		samples = append(samples, sm.Sample)
	}
	return true, samples
}
