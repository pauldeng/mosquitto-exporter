package main

// Phase 0 safety net: these tests pin TODAY's behaviour (bugs included) so
// later phases can prove exactly what they changed. See DESIGN.md §7 Phase 0
// and §3.1 for the defect IDs (C1-C5) referenced in comments below.

import (
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var updateGolden = flag.Bool("update", false, "update testdata golden files")

// resetGlobalState clears the package-level counterMetrics/gaugeMetrics maps
// and unregisters their collectors from the default Prometheus registry, so
// each test starts from a clean slate. This is a workaround for a real
// design problem (global mutable registry state, fixed properly in Phase 1
// by the value-store collector) - not something Phase 0 is allowed to fix.
func resetGlobalState(tb testing.TB) {
	tb.Helper()
	for topic, m := range counterMetrics {
		prometheus.Unregister(m)
		delete(counterMetrics, topic)
	}
	for topic, m := range gaugeMetrics {
		prometheus.Unregister(m)
		delete(gaugeMetrics, topic)
	}
}

// TestParseTopic pins parseTopic's output for all 56 real Mosquitto 2.1.2
// $SYS topics. The inventory was extracted directly from src/sys_tree.c in
// the eclipse-mosquitto v2.1.2 tag (99fa50f30e, the commit DESIGN.md cites):
// 24 metrics[] entries + 3 deprecated aliases + 27 load/** EWMAs + version +
// uptime = 56, matching DESIGN.md §4/§7.
func TestParseTopic(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string // topic doubles as the subtest name
		topic string
		want  string
	}{
		{name: "clients/total", topic: "$SYS/broker/clients/total", want: "broker_clients_total"},
		{name: "clients/maximum", topic: "$SYS/broker/clients/maximum", want: "broker_clients_maximum"},
		{name: "clients/disconnected", topic: "$SYS/broker/clients/disconnected", want: "broker_clients_disconnected"},
		{name: "clients/inactive (deprecated alias)", topic: "$SYS/broker/clients/inactive", want: "broker_clients_inactive"},
		{name: "clients/connected", topic: "$SYS/broker/clients/connected", want: "broker_clients_connected"},
		{name: "clients/active (deprecated alias)", topic: "$SYS/broker/clients/active", want: "broker_clients_active"},
		{name: "clients/expired", topic: "$SYS/broker/clients/expired", want: "broker_clients_expired"},
		{name: "messages/stored", topic: "$SYS/broker/messages/stored", want: "broker_messages_stored"},
		{name: "store/messages/count (alias of messages/stored)", topic: "$SYS/broker/store/messages/count", want: "broker_store_messages_count"},
		{name: "store/messages/bytes", topic: "$SYS/broker/store/messages/bytes", want: "broker_store_messages_bytes"},
		{name: "subscriptions/count", topic: "$SYS/broker/subscriptions/count", want: "broker_subscriptions_count"},
		// Awkward case #1: real topic string has an underscore already baked in.
		{name: "shared_subscriptions/count (underscore in topic)", topic: "$SYS/broker/shared_subscriptions/count", want: "broker_shared_subscriptions_count"},
		// Awkward case #2: real topic string has a literal space ("retained messages").
		// parseTopic's " " -> "_" pass happens to make this collide with the
		// clean name - today that's accidental, not intentional.
		{name: "retained messages/count (literal space in topic)", topic: "$SYS/broker/retained messages/count", want: "broker_retained_messages_count"},
		{name: "heap/current", topic: "$SYS/broker/heap/current", want: "broker_heap_current"},
		{name: "heap/maximum", topic: "$SYS/broker/heap/maximum", want: "broker_heap_maximum"},
		{name: "messages/received", topic: "$SYS/broker/messages/received", want: "broker_messages_received"},
		{name: "messages/sent", topic: "$SYS/broker/messages/sent", want: "broker_messages_sent"},
		{name: "bytes/received", topic: "$SYS/broker/bytes/received", want: "broker_bytes_received"},
		{name: "bytes/sent", topic: "$SYS/broker/bytes/sent", want: "broker_bytes_sent"},
		{name: "publish/bytes/received", topic: "$SYS/broker/publish/bytes/received", want: "broker_publish_bytes_received"},
		{name: "publish/bytes/sent", topic: "$SYS/broker/publish/bytes/sent", want: "broker_publish_bytes_sent"},
		{name: "packet/out/count", topic: "$SYS/broker/packet/out/count", want: "broker_packet_out_count"},
		{name: "packet/out/bytes", topic: "$SYS/broker/packet/out/bytes", want: "broker_packet_out_bytes"},
		{name: "connections/socket/count", topic: "$SYS/broker/connections/socket/count", want: "broker_connections_socket_count"},
		{name: "publish/messages/dropped", topic: "$SYS/broker/publish/messages/dropped", want: "broker_publish_messages_dropped"},
		{name: "publish/messages/received", topic: "$SYS/broker/publish/messages/received", want: "broker_publish_messages_received"},
		{name: "publish/messages/sent", topic: "$SYS/broker/publish/messages/sent", want: "broker_publish_messages_sent"},
		{name: "load/messages/received/1min", topic: "$SYS/broker/load/messages/received/1min", want: "broker_load_messages_received_1min"},
		{name: "load/messages/received/5min", topic: "$SYS/broker/load/messages/received/5min", want: "broker_load_messages_received_5min"},
		{name: "load/messages/received/15min", topic: "$SYS/broker/load/messages/received/15min", want: "broker_load_messages_received_15min"},
		{name: "load/messages/sent/1min", topic: "$SYS/broker/load/messages/sent/1min", want: "broker_load_messages_sent_1min"},
		{name: "load/messages/sent/5min", topic: "$SYS/broker/load/messages/sent/5min", want: "broker_load_messages_sent_5min"},
		{name: "load/messages/sent/15min", topic: "$SYS/broker/load/messages/sent/15min", want: "broker_load_messages_sent_15min"},
		{name: "load/publish/dropped/1min", topic: "$SYS/broker/load/publish/dropped/1min", want: "broker_load_publish_dropped_1min"},
		{name: "load/publish/dropped/5min", topic: "$SYS/broker/load/publish/dropped/5min", want: "broker_load_publish_dropped_5min"},
		{name: "load/publish/dropped/15min", topic: "$SYS/broker/load/publish/dropped/15min", want: "broker_load_publish_dropped_15min"},
		{name: "load/publish/received/1min", topic: "$SYS/broker/load/publish/received/1min", want: "broker_load_publish_received_1min"},
		{name: "load/publish/received/5min", topic: "$SYS/broker/load/publish/received/5min", want: "broker_load_publish_received_5min"},
		{name: "load/publish/received/15min", topic: "$SYS/broker/load/publish/received/15min", want: "broker_load_publish_received_15min"},
		{name: "load/publish/sent/1min", topic: "$SYS/broker/load/publish/sent/1min", want: "broker_load_publish_sent_1min"},
		{name: "load/publish/sent/5min", topic: "$SYS/broker/load/publish/sent/5min", want: "broker_load_publish_sent_5min"},
		{name: "load/publish/sent/15min", topic: "$SYS/broker/load/publish/sent/15min", want: "broker_load_publish_sent_15min"},
		{name: "load/bytes/received/1min", topic: "$SYS/broker/load/bytes/received/1min", want: "broker_load_bytes_received_1min"},
		{name: "load/bytes/received/5min", topic: "$SYS/broker/load/bytes/received/5min", want: "broker_load_bytes_received_5min"},
		{name: "load/bytes/received/15min", topic: "$SYS/broker/load/bytes/received/15min", want: "broker_load_bytes_received_15min"},
		{name: "load/bytes/sent/1min", topic: "$SYS/broker/load/bytes/sent/1min", want: "broker_load_bytes_sent_1min"},
		{name: "load/bytes/sent/5min", topic: "$SYS/broker/load/bytes/sent/5min", want: "broker_load_bytes_sent_5min"},
		{name: "load/bytes/sent/15min", topic: "$SYS/broker/load/bytes/sent/15min", want: "broker_load_bytes_sent_15min"},
		{name: "load/sockets/1min", topic: "$SYS/broker/load/sockets/1min", want: "broker_load_sockets_1min"},
		{name: "load/sockets/5min", topic: "$SYS/broker/load/sockets/5min", want: "broker_load_sockets_5min"},
		{name: "load/sockets/15min", topic: "$SYS/broker/load/sockets/15min", want: "broker_load_sockets_15min"},
		{name: "load/connections/1min", topic: "$SYS/broker/load/connections/1min", want: "broker_load_connections_1min"},
		{name: "load/connections/5min", topic: "$SYS/broker/load/connections/5min", want: "broker_load_connections_5min"},
		{name: "load/connections/15min", topic: "$SYS/broker/load/connections/15min", want: "broker_load_connections_15min"},
		{name: "version", topic: "$SYS/broker/version", want: "broker_version"},
		{name: "uptime", topic: "$SYS/broker/uptime", want: "broker_uptime"},
	}
	if len(tests) != 56 {
		t.Fatalf("test inventory has %d topics, want 56 (see DESIGN.md §4/§7)", len(tests))
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseTopic(tt.topic); got != tt.want {
				t.Errorf("parseTopic(%q) = %q, want %q", tt.topic, got, tt.want)
			}
		})
	}
}

// TestParseValue pins parseValue's output, including where it is wrong today.
func TestParseValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		payload string
		want    float64
	}{
		{name: "trailing unit text", payload: "1234 seconds", want: 1234},
		// Picks the first "digits.digits" run and stops there - never notices
		// the second ".2". A real defect in the parse (not one of C1-C5, but
		// worth pinning): this payload is never actually routed here in
		// production because "$SYS/broker/version" is in ignoreKeyMetrics.
		{name: "dotted version string", payload: "mosquitto version 2.1.2", want: 2.1},
		{name: "zero with decimals", payload: "0.00", want: 0},
		// Quirk: the regex's `-?` only applies to the decimal alternative
		// (`-?\d+\.\d+`), not the bare-integer alternative (`\d+`). A negative
		// integer therefore loses its sign; a negative decimal keeps it.
		{name: "negative integer loses its sign", payload: "-5", want: 5},
		{name: "negative decimal keeps its sign", payload: "-5.5", want: -5.5},
		// C5: parse failure is indistinguishable from a genuine zero sample.
		{name: "empty string (C5: silent 0)", payload: "", want: 0},
		{name: "non-numeric garbage (C5: silent 0)", payload: "garbage", want: 0},
		// C5 via a second path: strconv.ParseFloat's range error on an
		// out-of-float64-range literal is swallowed the same way as a parse
		// failure, again yielding an indistinguishable silent 0.
		{name: "huge value overflows float64 (C5: silent 0)", payload: strings.Repeat("9", 400), want: 0},
		{name: "huge but in-range value parses fine", payload: strings.Repeat("9", 20), want: 1e+20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := parseValue(tt.payload); got != tt.want {
				t.Errorf("parseValue(%q) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

// goldenMessages is the fixed set of $SYS messages fed through processUpdate
// for TestMetricsGolden. It deliberately covers: the counter path, the gauge
// path, both awkward real topic strings, and topics in ignoreKeyMetrics
// (which must produce no metric at all).
var goldenMessages = []struct {
	topic   string
	payload string
}{
	{"$SYS/broker/bytes/received", "1024"},
	{"$SYS/broker/messages/sent", "50"},
	// Pins the C-row bug from DESIGN.md §3.4: clients/total and uptime are
	// Counters today; the broker documents them as a running total / gauge.
	{"$SYS/broker/clients/total", "3"},
	{"$SYS/broker/uptime", "12345"},
	{"$SYS/broker/clients/connected", "2"},
	{"$SYS/broker/retained messages/count", "7"},
	{"$SYS/broker/shared_subscriptions/count", "0"},
	{"$SYS/broker/load/connections/1min", "0.25"},
	// Ignored topics: deprecated aliases and a topic that no longer exists on
	// a real 2.1.2 broker (DESIGN.md §3.4) but is still in ignoreKeyMetrics.
	// None of these three may produce a metric.
	{"$SYS/broker/version", "mosquitto version 2.1.2"},
	{"$SYS/broker/timestamp", "Thu Jan 01 1970"},
	{"$SYS/broker/clients/active", "2"},
}

// goldenMetricNames lists the metric names goldenMessages is expected to
// produce - used to filter the rendered /metrics body down to a
// deterministic subset for the golden-file comparison.
var goldenMetricNames = map[string]bool{
	"broker_bytes_received":             true,
	"broker_messages_sent":              true,
	"broker_clients_total":              true,
	"broker_uptime":                     true,
	"broker_clients_connected":          true,
	"broker_retained_messages_count":    true,
	"broker_shared_subscriptions_count": true,
	"broker_load_connections_1min":      true,
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

// TestMetricsGolden renders /metrics from a fixed set of injected messages
// and compares the deterministic mosquitto_-derived series against a golden
// file. It also asserts (without pinning volatile values) that at least one
// process_* and one go_* series is present, so their loss in Phase 4 fails
// CI instead of shipping silently (DESIGN.md §6.4).
//
// Not parallel: mutates the process-wide counterMetrics/gaugeMetrics maps
// and the default Prometheus registry, so it cannot run alongside other
// tests touching that shared state until Phase 1 removes it.
//
//nolint:paralleltest // shared global registry state, see comment above
func TestMetricsGolden(t *testing.T) {
	resetGlobalState(t)
	t.Cleanup(func() { resetGlobalState(t) })

	for _, m := range goldenMessages {
		processUpdate(m.topic, m.payload)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.Handler().ServeHTTP(rec, req)
	body := rec.Body.String()

	if !strings.Contains(body, "\nprocess_") && !strings.HasPrefix(body, "process_") {
		t.Error("no process_* series in /metrics output - lost exporter self-metrics")
	}
	if !strings.Contains(body, "\ngo_") && !strings.HasPrefix(body, "go_") {
		t.Error("no go_* series in /metrics output - lost exporter self-metrics")
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

// TestProcessUpdate_Race proves C1 and C2 (DESIGN.md §3.1): a plain float64
// written by the MQTT callback goroutine and read by promhttp's scrape
// goroutine (C1), and unsynchronised concurrent access to the
// counterMetrics/gaugeMetrics maps between the feeder and resetMetrics()
// - which runs on paho's OnConnectionLost goroutine in production (C2).
//
// Captured `go test -race -run TestProcessUpdate_Race` output (skip removed
// locally to run it), 2026-08-23:
// Actual `go test -race -run TestProcessUpdate_Race` output captured
// 2026-08-23 (skip removed locally to run it; trimmed to 2 of the 8 WARNING
// blocks plus the fatal crash - the full 388-line capture is quoted in full
// in the Phase 0 handoff report):
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
// merely a race." Phase 1's definition of done for C1/C2 is deleting the
// t.Skip below and having this test pass under -race.
//
// Not parallel: the entire point of this test is unsynchronised concurrent
// access to shared global state, so running it alongside other tests would
// only add unrelated noise to what it's proving.
//
//nolint:paralleltest // deliberately racy against shared global state, see comment above
func TestProcessUpdate_Race(t *testing.T) {
	t.Skip("fails today under -race: proves C1/C2 (see captured report above); unskip in Phase 1")

	resetGlobalState(t)
	t.Cleanup(func() { resetGlobalState(t) })

	const duration = 200 * time.Millisecond
	stop := time.After(duration)
	var wg sync.WaitGroup

	// Feeder: repeatedly creates brand-new topics (forces map writes, not
	// just updates) and updates an existing counter (drives C1).
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				processUpdate("$SYS/broker/bytes/received", "1")
				processUpdate("$SYS/broker/dynamic/"+strconv.Itoa(i), "1")
				i++
			}
		}
	}()

	// Scraper: repeatedly renders /metrics, invoking Collect() on every
	// registered MosquittoCounter concurrently with the feeder's Set calls.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/metrics", nil)
				promhttp.Handler().ServeHTTP(rec, req)
			}
		}
	}()

	// Resetter: simulates paho's OnConnectionLost callback goroutine, which
	// calls resetMetrics() concurrently with the feeder's map writes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				resetMetrics()
			}
		}
	}()

	wg.Wait()
}

// BenchmarkProcessUpdate measures the steady-state hot path: an already
// registered topic receiving a new value. This is what every message after
// the first hits, and per DESIGN.md §3.5 (P1) it still compiles a fresh
// regexp on every single call via parseValue - Phase 1 hoists that regexp to
// a package-level var. Baseline numbers recorded in the Phase 0 handoff
// report are the no-regression reference for gate G6.
func BenchmarkProcessUpdate(b *testing.B) {
	resetGlobalState(b)
	b.Cleanup(func() { resetGlobalState(b) })

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

	// Pre-warm outside the timed loop: register every topic once so the
	// benchmark measures only the "already registered" path (parseValue +
	// map lookup + Set), not one-time registration cost.
	for _, topic := range topics {
		processUpdate(topic, "0")
	}

	b.ReportAllocs()
	i := 0
	for b.Loop() {
		processUpdate(topics[i%len(topics)], payloads[i%len(payloads)])
		i++
	}
}
