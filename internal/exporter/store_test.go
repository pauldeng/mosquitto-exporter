package exporter

// Phase 0 safety net, since updated for Phase 1 and Phase 3: these tests
// pinned Phase 0's behaviour (bugs included); Phase 1 replaced the global
// counterMetrics/gaugeMetrics maps and per-topic dynamic registration with
// the Store type (store.go); Phase 3 rebuilt Store around sys.Resolve and
// the static export table (DESIGN.md §5.3/§5.4/§6). See DESIGN.md §7 for the
// phase-by-phase defect IDs (C1-C6) referenced in comments below.

import (
	"context"
	"flag"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/sys"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.uber.org/goleak"
)

var updateGolden = flag.Bool("update", false, "update testdata golden files")

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// sharedStore is registered into the default Prometheus registry exactly
// once for tests that verify the legacy exposition. Production uses its own
// registry. Store is an "unchecked" Collector - Describe sends no
// descriptors, since its metric set is determined by broker-controlled topics at runtime - and
// client_golang has no supported way to unregister an unchecked collector
// (registry.go tracks them in a plain slice with no removal path), so tests
// that need the default registry share this one instance and clear its
// state between runs instead of registering a fresh Store per test.
var sharedStore = NewStore(10 * time.Second)

func init() {
	prometheus.MustRegister(sharedStore)
}

// resetStore clears a Store back to its zero state. White-box (same
// package) test helper, standing in for the old resetGlobalState.
func resetStore(s *Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.samples = make(map[string]cachedSample)
	s.connected = false
	s.lastUptime = time.Time{}
	s.invalidPayloads = 0
	s.unknownTopics = 0
	s.droppedTopics = 0
}

// renderStore registers s into a throwaway private registry, renders
// /metrics from it, and returns the deterministic subset of the body
// matching names. A private registry (not the default one) is fine here
// since none of TestStore needs process_*/go_* alongside it.
//
// Since Phase 4 (DESIGN.md §7), Store.Collect only renders mosquitto_up and
// the mosquitto_exporter_* self-metric counters - mosquitto_broker_* series
// are rendered by the OTel instruments in otelmetrics.go instead, so tests
// that need those must read Store.BrokerSnapshot directly (see findSample)
// rather than going through renderStore.
func renderStore(s *Store, names map[string]bool) string {
	reg := prometheus.NewRegistry()
	reg.MustRegister(s)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)

	return filterMetricFamilies(rec.Body.String(), names)
}

// findSample returns the sys.Sample named name from samples, or fails the
// test - a small helper for asserting against Store.BrokerSnapshot's return
// value directly instead of scraping rendered Prometheus text.
func findSample(t *testing.T, samples []sys.Sample, name string) sys.Sample {
	t.Helper()
	for _, s := range samples {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no sample named %s in %+v", name, samples)
	return sys.Sample{}
}

// TestStore covers Store's behaviour directly: sys.Resolve wiring
// (OK/NotExported/InvalidPayload), the topic cache cap, and the connection
// lifecycle (DESIGN.md §5.3/§5.4).
func TestStore(t *testing.T) {
	t.Parallel()

	t.Run("gauge and counter classification, ignored topic, mosquitto_up", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		s.Update("$SYS/broker/bytes/received", "1024") // counter
		s.Update("$SYS/broker/clients/connected", "2") // gauge
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "5") // heartbeat: also renders as a gauge

		// mosquitto_up is still Store's own self-metric (Collect).
		got := renderStore(s, map[string]bool{"mosquitto_up": true})
		if !metricLinesMatch(got, "# TYPE mosquitto_up gauge\nmosquitto_up 1\n") {
			t.Errorf("got:\n%s\nwant mosquitto_up 1", got)
		}

		// Since Phase 4, mosquitto_broker_* values live in the cache exposed
		// via BrokerSnapshot, not in Store's own Prometheus rendering.
		up, samples := s.BrokerSnapshot()
		if !up {
			t.Fatal("expected up() to be true")
		}
		if v := findSample(t, samples, "mosquitto_broker_bytes_received_total"); v.Value != 1024 || v.Kind != sys.Counter {
			t.Errorf("bytes_received_total = %+v, want value 1024, kind Counter", v)
		}
		if v := findSample(t, samples, "mosquitto_broker_clients_connected"); v.Value != 2 || v.Kind != sys.Gauge {
			t.Errorf("clients_connected = %+v, want value 2, kind Gauge", v)
		}
	})

	t.Run("invalid payload is counted, not recorded (C5)", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		s.Update("$SYS/broker/bytes/received", "garbage")
		s.Update("$SYS/broker/bytes/received", "still garbage")
		s.SetConnected(true)

		got := renderStore(s, map[string]bool{"mosquitto_exporter_invalid_payloads_total": true})
		if !strings.Contains(got, "mosquitto_exporter_invalid_payloads_total 2") {
			t.Errorf("want mosquitto_exporter_invalid_payloads_total 2; got:\n%s", got)
		}

		// White-box: check the cache directly rather than via BrokerSnapshot,
		// which would trivially report nothing while down regardless of
		// whether the topic was cached.
		s.mu.RLock()
		_, cached := s.samples["$SYS/broker/bytes/received"]
		s.mu.RUnlock()
		if cached {
			t.Error("a topic that never parsed must never produce a cached sample (C5)")
		}
	})

	t.Run("unknown topic is counted, not recorded", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		// A topic never in the static export table (DESIGN.md §5.4): removed
		// in real Mosquitto since 1.5 (§3.4) but historically special-cased.
		s.Update("$SYS/broker/timestamp", "Thu Jan 01 1970")
		s.Update("$SYS/broker/does/not/exist", "1")

		s.mu.RLock()
		defer s.mu.RUnlock()
		if len(s.samples) != 0 {
			t.Errorf("unknown topics must never be cached per-topic: len(samples)=%d", len(s.samples))
		}
		if s.unknownTopics != 2 {
			t.Errorf("unknownTopics = %d, want 2", s.unknownTopics)
		}
	})

	t.Run("dropped load/** series are not exported (DESIGN.md §6.3, decision 10.4)", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		// Only load/connections is kept; the other 8 load/** families (24
		// series across three windows) are dropped entirely.
		s.Update("$SYS/broker/load/messages/received/1min", "5.0")

		s.mu.RLock()
		defer s.mu.RUnlock()
		if len(s.samples) != 0 {
			t.Errorf("a dropped load/** topic must not be cached: len(samples)=%d", len(s.samples))
		}
		// Deliberately dropped, so NOT counted as unknown: these arrive every
		// sys_interval on a healthy broker, and counting them would make
		// mosquitto_exporter_unknown_topics_total climb by 24 per tick,
		// burying the one signal it exists to give (§5.4).
		if s.unknownTopics != 0 {
			t.Errorf("unknownTopics = %d after a deliberately dropped series, want 0", s.unknownTopics)
		}
	})

	t.Run("aliased topics collapse to one series, not a duplicate (DESIGN.md §6.2, 2->1)", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "1")
		s.Update("$SYS/broker/messages/stored", "41")
		s.Update("$SYS/broker/store/messages/count", "42")

		up, samples := s.BrokerSnapshot()
		if !up {
			t.Fatal("expected up() to be true")
		}
		n := 0
		for _, sm := range samples {
			if sm.Name == "mosquitto_broker_messages_stored" {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("mosquitto_broker_messages_stored appears %d times, want exactly 1 despite two source topics; samples:\n%+v", n, samples)
		}
		if v := findSample(t, samples, "mosquitto_broker_messages_stored"); v.Value != 42 {
			t.Errorf("collapsed alias did not retain the latest value; got %v, want 42", v.Value)
		}
		s.mu.RLock()
		cached := len(s.samples)
		s.mu.RUnlock()
		if cached != 2 { // uptime + one collapsed messages_stored series
			t.Errorf("cached samples = %d, want 2", cached)
		}
	})

	t.Run("broker info label changes after an upgrade", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "1")
		s.Update("$SYS/broker/version", "mosquitto version 2.1.2")
		s.Update("$SYS/broker/version", "mosquitto version 2.1.3")

		_, samples := s.BrokerSnapshot()
		info := findSample(t, samples, "mosquitto_broker_info")
		if info.Labels["mosquitto_version"] != "2.1.3" {
			t.Errorf("broker info retained a stale version label; got %+v", info)
		}
	})

	t.Run("SetConnected(false) reports mosquitto_up 0 and omits broker_* series (§5.3)", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "1")
		s.Update("$SYS/broker/bytes/received", "1024")
		s.SetConnected(false)

		got := renderStore(s, map[string]bool{"mosquitto_up": true})
		if !strings.Contains(got, "mosquitto_up 0") {
			t.Errorf("want mosquitto_up 0; got:\n%s", got)
		}
		if up, samples := s.BrokerSnapshot(); up || samples != nil {
			t.Errorf("disconnected must omit every mosquitto_broker_* series, not report it flat or zeroed; up=%v samples=%+v", up, samples)
		}

		// The value is still cached, not forgotten - it must reappear
		// unchanged once reconnected. That reconnection path is exercised by
		// TestStore_HeartbeatStallRecovers below.
		s.mu.RLock()
		_, known := s.samples["$SYS/broker/bytes/received"]
		s.mu.RUnlock()
		if !known {
			t.Error("disconnect must preserve the cached value")
		}

		// A late message from the closed session must not refresh its heartbeat,
		// and a new session must prove liveness for itself.
		s.Update("$SYS/broker/uptime", "2")
		s.SetConnected(true)
		got = renderStore(s, map[string]bool{"mosquitto_up": true})
		if up, _ := s.BrokerSnapshot(); !strings.Contains(got, "mosquitto_up 0") || up {
			t.Errorf("reconnect exposed cached broker data before a new heartbeat; got:\n%s, up=%v", got, up)
		}
		s.Update("$SYS/broker/uptime", "3")
		got = renderStore(s, map[string]bool{"mosquitto_up": true})
		up, samples := s.BrokerSnapshot()
		if !strings.Contains(got, "mosquitto_up 1") || !up {
			t.Errorf("fresh heartbeat did not restore up state; got:\n%s, up=%v", got, up)
		}
		if v := findSample(t, samples, "mosquitto_broker_bytes_received_total"); v.Value != 1024 {
			t.Errorf("fresh heartbeat did not restore cached broker data; got %v, want 1024", v.Value)
		}
	})

	t.Run("cache is capped, but an already-tracked topic keeps updating (DESIGN.md §5.4)", func(t *testing.T) {
		t.Parallel()
		s := NewStore(10 * time.Second)

		const trackedTopic = "$SYS/broker/bytes/received" // real, exportable, Counter
		s.Update(trackedTopic, "1")

		// Pad the cache to the cap with synthetic entries via direct map
		// access (white-box): internal/sys's real static export table has
		// far fewer than maxTrackedTopics exportable topics, so there is no
		// way to reach the cap through Update alone with real topic strings.
		s.mu.Lock()
		for i := len(s.samples); i < maxTrackedTopics; i++ {
			key := fmt.Sprintf("synthetic/%d", i)
			s.samples[key] = cachedSample{desc: prometheus.NewDesc(fmt.Sprintf("synthetic_%d", i), "synthetic", nil, nil)}
		}
		s.mu.Unlock()

		// An already-tracked topic must still update once the cache is full.
		s.Update(trackedTopic, "2")
		s.mu.RLock()
		got := s.samples[trackedTopic].Value
		droppedBefore := s.droppedTopics
		s.mu.RUnlock()
		if got != 2 {
			t.Errorf("already-tracked topic did not update at cap: value=%v, want 2", got)
		}

		// A brand-new topic must be dropped and counted, never evicting an
		// existing entry.
		const newTopic = "$SYS/broker/messages/sent"
		s.Update(newTopic, "5")
		s.mu.RLock()
		defer s.mu.RUnlock()
		if _, ok := s.samples[newTopic]; ok {
			t.Error("new topic was cached despite a full cache")
		}
		if s.droppedTopics != droppedBefore+1 {
			t.Errorf("droppedTopics = %d, want %d", s.droppedTopics, droppedBefore+1)
		}
		if len(s.samples) != maxTrackedTopics {
			t.Errorf("cache grew beyond cap: len=%d", len(s.samples))
		}
	})
}

// TestStore_HeartbeatStallReportsDown is the §5.3 stall-detection proof:
// connected, but $SYS/broker/uptime has not advanced for 3x sys-interval, so
// the broker's $SYS tree is treated as stalled and reported down. Uses
// testing/synctest so the 3x sys-interval wait is instant and deterministic
// instead of a real sleep.
func TestStore_HeartbeatStallReportsDown(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const sysInterval = 10 * time.Second
		s := NewStore(sysInterval)
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "1")

		if !s.up() {
			t.Fatal("expected up() immediately after a fresh uptime tick")
		}

		time.Sleep(3*sysInterval + time.Second)
		synctest.Wait()

		if s.up() {
			t.Error("uptime stalled for over 3x sys-interval while connected: want down, got up")
		}
		got := renderStore(s, map[string]bool{"mosquitto_up": true})
		if !strings.Contains(got, "mosquitto_up 0") {
			t.Errorf("want mosquitto_up 0 on heartbeat stall; got:\n%s", got)
		}
	})
}

// TestStore_HeartbeatStallRecovers proves the stall is not sticky: once
// uptime ticks resume, up() recovers without needing SetConnected to
// toggle - matching a broker whose $SYS tree pauses and resumes while the
// TCP session never drops.
func TestStore_HeartbeatStallRecovers(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const sysInterval = 10 * time.Second
		s := NewStore(sysInterval)
		s.SetConnected(true)
		s.Update("$SYS/broker/uptime", "1")

		time.Sleep(3*sysInterval + time.Second)
		synctest.Wait()
		if s.up() {
			t.Fatal("expected down after the stall window elapsed")
		}

		s.Update("$SYS/broker/uptime", "2")
		if !s.up() {
			t.Error("a fresh uptime tick must clear the stall immediately")
		}
	})
}

// TestStore_UnchangingTopicKeepsReporting is the DESIGN.md §5.3 regression
// test explicitly required by the exporter's staleness contract: it fails
// under a per-topic recency rule (an earlier, wrong draft) and passes under
// connection-state gating. A healthy broker is change-gated - a
// legitimately constant value (e.g. publish_messages_dropped sitting at 0)
// may go many sys-intervals without a republish, and that must not make its
// series vanish. Only the uptime heartbeat's own staleness matters.
func TestStore_UnchangingTopicKeepsReporting(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		const sysInterval = 10 * time.Second
		s := NewStore(sysInterval)
		s.SetConnected(true)

		// Set once, then never touched again - simulating a stable,
		// change-gated $SYS value.
		s.Update("$SYS/broker/shared_subscriptions/count", "0")

		// The broker keeps ticking (uptime advances every tick) for 10x
		// sys-interval, well past the 3x heartbeat window, while the other
		// topic's value never changes.
		for i := 1; i <= 10; i++ {
			time.Sleep(sysInterval)
			s.Update("$SYS/broker/uptime", strconv.Itoa(i*int(sysInterval.Seconds())))
		}
		synctest.Wait()

		if !s.up() {
			t.Fatal("heartbeat is fresh (just ticked); want up")
		}
		up, samples := s.BrokerSnapshot()
		if !up {
			t.Fatal("expected up() to be true")
		}
		if v := findSample(t, samples, "mosquitto_broker_shared_subscriptions"); v.Value != 0 {
			t.Errorf("an unchanging-but-healthy series must still be reported after 10x sys-interval; got %v, want 0", v.Value)
		}
	})
}

// goldenMessages is the fixed set of $SYS messages fed through
// sharedStore.Update for TestMetricsGolden. Chosen to cover every category
// of Phase 3 change (DESIGN.md §6): an unchanged counter and gauge, all four
// Counter<->Gauge reclassifications (§3.4), the messages_stored alias
// collapse (2->1, §6.2), the load/connections window-label shape change
// (§6.3), the new mosquitto_broker_info metric (version was previously
// ignored entirely), a topic among the 24 dropped load/** series, and a
// topic that no longer exists on a real broker (feeds
// mosquitto_exporter_unknown_topics_total instead of silently vanishing).
var goldenMessages = []struct {
	topic   string
	payload string
}{
	{"$SYS/broker/bytes/received", "1024"},             // counter, unchanged
	{"$SYS/broker/messages/sent", "50"},                // counter, unchanged
	{"$SYS/broker/clients/total", "3"},                 // was Counter -> Gauge (§3.4)
	{"$SYS/broker/uptime", "12345"},                    // was Counter -> Gauge; also the heartbeat
	{"$SYS/broker/clients/connected", "2"},             // gauge, unchanged
	{"$SYS/broker/retained messages/count", "7"},       // gauge; awkward literal space in topic
	{"$SYS/broker/shared_subscriptions/count", "0"},    // gauge, unchanged
	{"$SYS/broker/load/connections/1min", "0.25"},      // shape change: window="1m" label
	{"$SYS/broker/messages/stored", "42"},              // alias pair, half A
	{"$SYS/broker/store/messages/count", "42"},         // alias pair, half B -> must collapse to 1 series
	{"$SYS/broker/connections/socket/count", "9"},      // was Gauge -> Counter (§3.4)
	{"$SYS/broker/clients/expired", "1"},               // was Gauge -> Counter (§3.4)
	{"$SYS/broker/version", "mosquitto version 2.1.2"}, // new: mosquitto_broker_info (was ignored)
	{"$SYS/broker/load/messages/received/1min", "5.0"}, // one of the 24 dropped load/** series
	{"$SYS/broker/timestamp", "Thu Jan 01 1970"},       // no longer exists on a real broker (§3.4)
}

// goldenMetricNames lists the metric names goldenMessages (plus the fixed
// mosquitto_up / mosquitto_exporter_* series every Store exposes) are
// expected to produce - used to filter the rendered /metrics body down to a
// deterministic subset for the golden-file comparison.
var goldenMetricNames = map[string]bool{
	"mosquitto_broker_bytes_received_total":     true,
	"mosquitto_broker_messages_sent_total":      true,
	"mosquitto_broker_clients_total":            true,
	"mosquitto_broker_uptime_seconds":           true,
	"mosquitto_broker_clients_connected":        true,
	"mosquitto_broker_retained_messages":        true,
	"mosquitto_broker_shared_subscriptions":     true,
	"mosquitto_broker_load_connections":         true,
	"mosquitto_broker_messages_stored":          true,
	"mosquitto_broker_connections_socket_total": true,
	"mosquitto_broker_clients_expired_total":    true,
	"mosquitto_broker_info":                     true,
	"mosquitto_up":                              true,
	"mosquitto_exporter_invalid_payloads_total": true,
	"mosquitto_exporter_unknown_topics_total":   true,
	"mosquitto_exporter_dropped_topics_total":   true,
}

// metricLinesMatch compares got against want ignoring HELP text (owned by
// internal/sys, not pinned here) but requiring identical TYPE and value
// lines in the same order.
func metricLinesMatch(got, want string) bool {
	filter := func(body string) []string {
		var lines []string
		for line := range strings.SplitSeq(body, "\n") {
			if line != "" && !strings.HasPrefix(line, "# HELP") {
				lines = append(lines, line)
			}
		}
		return lines
	}
	g, w := filter(got), filter(want)
	if len(g) != len(w) {
		return false
	}
	for i := range g {
		if g[i] != w[i] {
			return false
		}
	}
	return true
}

// filterMetricFamilies keeps only the "# HELP name / # TYPE name / value..."
// blocks for the given metric names, in the order they appear in body.
// Plain stdlib string handling - deliberately not using promhttp/expfmt
// machinery, since the render is already plain text and we only need exact
// substring blocks, not semantic parsing.
func filterMetricFamilies(body string, names map[string]bool) string {
	var out strings.Builder
	keep := false
	for line := range strings.SplitSeq(body, "\n") {
		if after, ok := strings.CutPrefix(line, "# HELP "); ok {
			name, _, _ := strings.Cut(after, " ")
			keep = names[name]
		}
		if keep && line != "" {
			out.WriteString(line)
			out.WriteString("\n")
		}
	}
	return out.String()
}

// TestMetricsGolden builds the exact production pipeline (DESIGN.md §7
// Phase 4) - a fresh Store and client_golang's process/Go collectors
// (§6.4) alongside the OTel MeterProvider and Prometheus reader from
// otelprom.go/otelmetrics.go, all in one registry, exactly as Run wires
// them in server.go - feeds it a fixed set of injected messages, and
// compares the deterministic mosquitto_-derived series against a golden
// file. This is the byte-identity proof the engine swap exists to satisfy:
// the golden file itself must never change (see testdata/metrics.golden).
//
// It also asserts (without pinning volatile values) that at least one
// process_* and one go_* series is present, so their loss in a later phase
// fails CI instead of shipping silently (DESIGN.md §6.4).
//
// Uses its own local Store and registry rather than sharedStore/the default
// registry, so - unlike TestProcessUpdate_Race and BenchmarkProcessUpdate -
// it shares no global state and is safe to run in parallel.
func TestMetricsGolden(t *testing.T) {
	t.Parallel()

	store := NewStore(10 * time.Second)
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		store,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	promReader, err := newPrometheusReader(registry)
	if err != nil {
		t.Fatalf("newPrometheusReader: %v", err)
	}
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(promReader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	if err := registerBrokerMetrics(meterProvider.Meter(otelMeterName), store); err != nil {
		t.Fatalf("registerBrokerMetrics: %v", err)
	}

	store.SetConnected(true)
	for _, m := range goldenMessages {
		store.Update(m.topic, m.payload)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.HandlerFor(registry, promhttp.HandlerOpts{}).ServeHTTP(rec, req)
	body := rec.Body.String()

	if rec.Code != 200 {
		t.Fatalf("scrape status = %d, want 200 (body:\n%s)", rec.Code, body)
	}
	if !strings.Contains(body, "\nprocess_") && !strings.HasPrefix(body, "process_") {
		t.Error("no process_* series in /metrics output - lost exporter self-metrics")
	}
	if !strings.Contains(body, "\ngo_") && !strings.HasPrefix(body, "go_") {
		t.Error("no go_* series in /metrics output - lost exporter self-metrics")
	}
	// The 24 dropped load/** series (DESIGN.md §6.3, decision 10.4) must
	// never appear, under any name derived from that topic.
	if strings.Contains(body, "load_messages_received") {
		t.Error("a deliberately dropped load/** topic produced a series")
	}
	// The alias pair must render as exactly one series (DESIGN.md §6.2).
	if n := strings.Count(body, "# TYPE mosquitto_broker_messages_stored "); n != 1 {
		t.Errorf("mosquitto_broker_messages_stored TYPE line appears %d times, want 1", n)
	}

	got := filterMetricFamilies(body, goldenMetricNames)
	goldenPath := filepath.Join("testdata", "metrics.golden")

	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(got), 0o600); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
	}

	//nolint:gosec // G304: goldenPath is a fixed testdata/ path built from
	// two constant strings, never from external input.
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("reading golden file: %v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("rendered /metrics does not match %s (run with -update to refresh it after a deliberate change)\n--- got ---\n%s\n--- want ---\n%s", goldenPath, got, string(want))
	}
}

func TestBrokerMetricUnits(t *testing.T) {
	t.Parallel()
	store := NewStore(time.Minute)
	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = meterProvider.Shutdown(context.Background()) })
	if err := registerBrokerMetrics(meterProvider.Meter(otelMeterName), store); err != nil {
		t.Fatalf("registerBrokerMetrics: %v", err)
	}
	store.SetConnected(true)
	store.Update("$SYS/broker/uptime", "1")
	store.Update("$SYS/broker/bytes/received", "2")
	store.Update("$SYS/broker/clients/connected", "3")

	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := map[string]string{
		"mosquitto_broker_uptime_seconds":       "s",
		"mosquitto_broker_bytes_received_total": "By",
		"mosquitto_broker_clients_connected":    "1",
	}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if unit, ok := want[metric.Name]; ok {
				if metric.Unit != unit {
					t.Errorf("%s unit = %q, want %q", metric.Name, metric.Unit, unit)
				}
				delete(want, metric.Name)
			}
		}
	}
	if len(want) != 0 {
		t.Errorf("metrics not collected: %v", want)
	}
}

// TestProcessUpdate_Race proves C1 and C2 are fixed (DESIGN.md §3.1): a
// feeder goroutine calling Store.Update concurrently with a scraper
// rendering /metrics (which calls Store.Collect) and reading
// Store.BrokerSnapshot (which - since Phase 4 - is what actually ranges
// over the samples map that C1/C2 were originally about; Collect itself no
// longer touches it) and a connection-state flapper calling
// Store.SetConnected - simulating paho's OnConnect / OnConnectionLost
// callbacks in production. All three now go through the same sync.RWMutex
// in store.go.
//
// Captured `go test -race -run TestProcessUpdate_Race` output from Phase 0,
// before this fix, 2026-08-23 (trimmed to 2 of the 8 WARNING blocks plus the
// fatal crash - the full 388-line capture is quoted in full in the Phase 0
// handoff report):
//
//	==================
//	WARNING: DATA RACE
//	Read at 0x00c0001280a8 by goroutine 23:
//	  github.com/sapcc/mosquitto-exporter.(*MosquittoCounter).Collect()
//	      mosquitto_counter.go:38 +0x47
//	  github.com/prometheus/client_golang/prometheus.(*Registry).Gather.func1()
//	      .../prometheus/registry.go:456 +0x12a
//
//	Previous write at 0x00c0001280a8 by goroutine 13:
//	  github.com/sapcc/mosquitto-exporter.(*counter).Set()
//	      mosquitto_counter.go:50 +0x16c
//	  github.com/sapcc/mosquitto-exporter.(*MosquittoCounter).Set()
//	      mosquitto_counter.go:25 +0x162
//	  github.com/sapcc/mosquitto-exporter.resetMetrics()
//	      main.go:127 +0x117
//	  github.com/sapcc/mosquitto-exporter.TestProcessUpdate_Race.func4()
//	      main_test.go:373 +0x88
//	==================                                    <- proves C1
//	==================
//	WARNING: DATA RACE
//	Write at 0x00c0001a65d0 by goroutine 11:
//	  runtime.mapassign_faststr()
//	  github.com/sapcc/mosquitto-exporter.processCounterMetric()
//	      main.go:251 +0x2e9
//	  github.com/sapcc/mosquitto-exporter.processUpdate()
//	      main.go:229 +0xc6
//
//	Previous read at 0x00c0001a65d0 by goroutine 13:
//	  runtime.mapIterStart()
//	  github.com/sapcc/mosquitto-exporter.resetMetrics()
//	      main.go:125 +0x67
//	==================                                    <- proves C2
//	[... 6 more WARNING: DATA RACE blocks omitted, same two shapes ...]
//	fatal error: concurrent map read and map write
//
//	goroutine 11 [running]:
//	internal/runtime/maps.fatal(...)
//	github.com/sapcc/mosquitto-exporter.resetMetrics()
//		main.go:131 +0x247
//	github.com/sapcc/mosquitto-exporter.TestProcessUpdate_Race.func4()
//		main_test.go:373 +0x89
//	FAIL	github.com/sapcc/mosquitto-exporter	0.088s
//
// The fatal crash (not just a WARNING) is C2 exactly as documented in
// DESIGN.md §3.1: "Concurrent map read+write is a fatal runtime throw, not
// merely a race." Phase 1 replaced the racy globals with Store's
// mutex-guarded map, so this test is unskipped and passes under -race - see
// the verification output in the Phase 1 handoff report.
//
// Not parallel: the entire point of this test is concurrent access to
// shared state from three goroutines at once, so running it alongside other
// tests would only add unrelated noise to what it's proving.
//
//nolint:paralleltest // deliberately concurrent against shared state, see comment above
func TestProcessUpdate_Race(t *testing.T) {
	resetStore(sharedStore)
	t.Cleanup(func() { resetStore(sharedStore) })

	// close(done) broadcasts to every receiver. time.After does NOT: its
	// channel yields exactly one value, so with three goroutines selecting
	// on it, one exits and the other two spin forever (hang + unbounded
	// allocation). That bug OOM-killed this test at 4.6GB before it was
	// caught; do not "simplify" this back to time.After.
	const duration = 200 * time.Millisecond
	done := make(chan struct{})
	time.AfterFunc(duration, func() { close(done) })
	var wg sync.WaitGroup

	// Feeder: repeatedly creates brand-new topics (forces map writes, not
	// just updates) and updates an existing counter (drives C1).
	wg.Go(func() {
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				sharedStore.Update("$SYS/broker/bytes/received", "1")
				sharedStore.Update("$SYS/broker/uptime", strconv.Itoa(i))
				i++
			}
		}
	})

	// Scraper: repeatedly renders /metrics (Store.Collect) and reads
	// BrokerSnapshot (which ranges over the samples map, the C1/C2 map
	// itself), both concurrently with the feeder's Update calls.
	wg.Go(func() {
		for {
			select {
			case <-done:
				return
			default:
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/metrics", nil)
				promhttp.Handler().ServeHTTP(rec, req)
				sharedStore.BrokerSnapshot()
			}
		}
	})

	// Flapper: simulates paho's OnConnect/OnConnectionLost callbacks flipping
	// connection state concurrently with the feeder's map writes.
	wg.Go(func() {
		connected := false
		for {
			select {
			case <-done:
				return
			default:
				connected = !connected
				sharedStore.SetConnected(connected)
			}
		}
	})

	wg.Wait()
}

// BenchmarkProcessUpdate measures the steady-state hot path: an already-seen
// topic receiving a new value. This is what every message after the first
// hits. Per DESIGN.md §3.5 (P1), Phase 0's version still compiled a fresh
// regexp on every single call via parseValue; Phase 1 hoisted that regexp
// (and parseTopic's replacer, P2) to package-level vars, now owned by
// internal/sys. Baseline numbers recorded in the Phase 0 handoff report
// (3.8 µs/op, 37 allocs/op) are the no-regression reference for gate G6.
func BenchmarkProcessUpdate(b *testing.B) {
	resetStore(sharedStore)
	b.Cleanup(func() { resetStore(sharedStore) })

	topics := []string{
		"$SYS/broker/bytes/received",
		"$SYS/broker/messages/sent",
		"$SYS/broker/clients/connected",
		"$SYS/broker/load/connections/1min",
	}
	payloads := make([]string, 1000)
	for i := range payloads {
		payloads[i] = strconv.Itoa(i)
	}

	// Pre-warm outside the timed loop: seed every topic once so the
	// benchmark measures only the "already seen" path (sys.Resolve + map
	// lookup + mutex), not one-time map-growth cost.
	for _, topic := range topics {
		sharedStore.Update(topic, "0")
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		sharedStore.Update(topics[i%len(topics)], payloads[i%len(payloads)])
		i++
	}
}

// TestStore_ExcludedTopicsDoNotCountAsUnknown pins the distinction between
// "deliberately dropped" and "unrecognised". Before this split, the 24
// dropped load/** series fed mosquitto_exporter_unknown_topics_total, so on
// a healthy broker it rose by 24 every sys_interval - permanently non-zero
// and rising, which buries a genuinely new topic in the noise.
func TestStore_ExcludedTopicsDoNotCountAsUnknown(t *testing.T) {
	t.Parallel()
	s := NewStore(10 * time.Second)

	// All 8 dropped load families; recognised, deliberately not exported.
	for _, topic := range []string{
		"$SYS/broker/load/messages/received/1min",
		"$SYS/broker/load/messages/sent/5min",
		"$SYS/broker/load/publish/dropped/15min",
		"$SYS/broker/load/publish/received/1min",
		"$SYS/broker/load/publish/sent/5min",
		"$SYS/broker/load/bytes/received/15min",
		"$SYS/broker/load/bytes/sent/1min",
		"$SYS/broker/load/sockets/5min",
	} {
		s.Update(topic, "1.5")
	}
	if got := s.unknownTopics; got != 0 {
		t.Errorf("unknownTopics = %d after only excluded topics, want 0", got)
	}

	// A genuinely unrecognised topic must still be counted, or the signal is
	// gone in the other direction.
	s.Update("$SYS/broker/something/nobody/knows", "1")
	if got := s.unknownTopics; got != 1 {
		t.Errorf("unknownTopics = %d after one unknown topic, want 1", got)
	}
}
