package sys

import (
	"maps"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// These are the highest-value tests in the project: Resolve is the single
// entry point every exported metric flows through, and 8 G2 sets this
// package's coverage floor at 90%.

// validMetricName and validLabelKey are the Prometheus exposition-format
// grammars (client_golang model.IsValidMetricName / LabelName), used to
// prove every name/label key this package produces would actually render.
var (
	validMetricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	validLabelKey   = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)
)

// topicCase is one entry of the static export table (DESIGN.md §6.1/§6.2),
// paired with a payload that resolves cleanly. This is the golden test that
// catches a wrong topic string: renaming, removing or reclassifying an
// entry in sys.go without updating this table fails here.
type topicCase struct {
	name    string // also the subtest name
	topic   string
	payload string
	want    Sample
}

var topicCases = []topicCase{
	// --- Counters ---
	{"bytes/received", "$SYS/broker/bytes/received", "123456",
		Sample{Name: "mosquitto_broker_bytes_received_total", Kind: Counter, Value: 123456}},
	{"bytes/sent", "$SYS/broker/bytes/sent", "654321",
		Sample{Name: "mosquitto_broker_bytes_sent_total", Kind: Counter, Value: 654321}},
	{"messages/received", "$SYS/broker/messages/received", "10",
		Sample{Name: "mosquitto_broker_messages_received_total", Kind: Counter, Value: 10}},
	{"messages/sent", "$SYS/broker/messages/sent", "9",
		Sample{Name: "mosquitto_broker_messages_sent_total", Kind: Counter, Value: 9}},
	{"publish/bytes/received", "$SYS/broker/publish/bytes/received", "500",
		Sample{Name: "mosquitto_broker_publish_bytes_received_total", Kind: Counter, Value: 500}},
	{"publish/bytes/sent", "$SYS/broker/publish/bytes/sent", "400",
		Sample{Name: "mosquitto_broker_publish_bytes_sent_total", Kind: Counter, Value: 400}},
	{"publish/messages/received", "$SYS/broker/publish/messages/received", "8",
		Sample{Name: "mosquitto_broker_publish_messages_received_total", Kind: Counter, Value: 8}},
	{"publish/messages/sent", "$SYS/broker/publish/messages/sent", "7",
		Sample{Name: "mosquitto_broker_publish_messages_sent_total", Kind: Counter, Value: 7}},
	{"publish/messages/dropped", "$SYS/broker/publish/messages/dropped", "0",
		Sample{Name: "mosquitto_broker_publish_messages_dropped_total", Kind: Counter, Value: 0}},
	// Fix (§3.4): was a Gauge.
	{"connections/socket/count (fixed to Counter)", "$SYS/broker/connections/socket/count", "3",
		Sample{Name: "mosquitto_broker_connections_socket_total", Kind: Counter, Value: 3}},
	// Fix (§3.4): was a Gauge.
	{"clients/expired (fixed to Counter)", "$SYS/broker/clients/expired", "2",
		Sample{Name: "mosquitto_broker_clients_expired_total", Kind: Counter, Value: 2}},

	// --- Gauges ---
	// Fix (§3.4): was a Counter.
	{"clients/total (fixed to Gauge)", "$SYS/broker/clients/total", "5",
		Sample{Name: "mosquitto_broker_clients_total", Kind: Gauge, Value: 5}},
	{"clients/connected", "$SYS/broker/clients/connected", "4",
		Sample{Name: "mosquitto_broker_clients_connected", Kind: Gauge, Value: 4}},
	{"clients/active (deprecated alias)", "$SYS/broker/clients/active", "4",
		Sample{Name: "mosquitto_broker_clients_connected", Kind: Gauge, Value: 4}},
	{"clients/disconnected", "$SYS/broker/clients/disconnected", "1",
		Sample{Name: "mosquitto_broker_clients_disconnected", Kind: Gauge, Value: 1}},
	{"clients/inactive (deprecated alias)", "$SYS/broker/clients/inactive", "1",
		Sample{Name: "mosquitto_broker_clients_disconnected", Kind: Gauge, Value: 1}},
	// Fix (§3.4): was a Counter.
	{"clients/maximum (fixed to Gauge)", "$SYS/broker/clients/maximum", "6",
		Sample{Name: "mosquitto_broker_clients_maximum", Kind: Gauge, Value: 6}},
	{"messages/stored", "$SYS/broker/messages/stored", "11",
		Sample{Name: "mosquitto_broker_messages_stored", Kind: Gauge, Value: 11}},
	{"store/messages/count (duplicate of messages/stored)", "$SYS/broker/store/messages/count", "11",
		Sample{Name: "mosquitto_broker_messages_stored", Kind: Gauge, Value: 11}},
	{"store/messages/bytes", "$SYS/broker/store/messages/bytes", "2048",
		Sample{Name: "mosquitto_broker_store_messages_bytes", Kind: Gauge, Value: 2048}},
	{"subscriptions/count", "$SYS/broker/subscriptions/count", "3",
		Sample{Name: "mosquitto_broker_subscriptions", Kind: Gauge, Value: 3}},
	{"shared_subscriptions/count", "$SYS/broker/shared_subscriptions/count", "1",
		Sample{Name: "mosquitto_broker_shared_subscriptions", Kind: Gauge, Value: 1}},
	{"retained messages/count (literal space in topic)", "$SYS/broker/retained messages/count", "0",
		Sample{Name: "mosquitto_broker_retained_messages", Kind: Gauge, Value: 0}},
	{"heap/current", "$SYS/broker/heap/current", "1048576",
		Sample{Name: "mosquitto_broker_heap_current_bytes", Kind: Gauge, Value: 1048576}},
	{"heap/maximum", "$SYS/broker/heap/maximum", "2097152",
		Sample{Name: "mosquitto_broker_heap_maximum_bytes", Kind: Gauge, Value: 2097152}},
	{"packet/out/count", "$SYS/broker/packet/out/count", "5",
		Sample{Name: "mosquitto_broker_packet_out_count", Kind: Gauge, Value: 5}},
	{"packet/out/bytes", "$SYS/broker/packet/out/bytes", "1024",
		Sample{Name: "mosquitto_broker_packet_out_bytes", Kind: Gauge, Value: 1024}},
	// Fix (§3.4): was a Counter. Payload carries a trailing unit ("seconds").
	{"uptime (fixed to Gauge)", "$SYS/broker/uptime", "12345 seconds",
		Sample{Name: "mosquitto_broker_uptime_seconds", Kind: Gauge, Value: 12345}},

	// --- load/connections: the one load/** series kept (§6.3) ---
	{"load/connections/1min", "$SYS/broker/load/connections/1min", "0.50",
		Sample{Name: "mosquitto_broker_load_connections", Kind: Gauge, Value: 0.50, Labels: map[string]string{"window": "1m"}}},
	{"load/connections/5min", "$SYS/broker/load/connections/5min", "0.75",
		Sample{Name: "mosquitto_broker_load_connections", Kind: Gauge, Value: 0.75, Labels: map[string]string{"window": "5m"}}},
	{"load/connections/15min", "$SYS/broker/load/connections/15min", "1.00",
		Sample{Name: "mosquitto_broker_load_connections", Kind: Gauge, Value: 1.00, Labels: map[string]string{"window": "15m"}}},
}

// droppedLoadTopics are the 24 load/** series that are pure EWMAs of a
// counter already exported above (§6.3, decision 10.4) and must never
// produce a Sample.
var droppedLoadTopics = func() []string {
	categories := []string{
		"messages/received", "messages/sent",
		"publish/dropped", "publish/received", "publish/sent",
		"bytes/received", "bytes/sent",
		"sockets",
	}
	windows := []string{"1min", "5min", "15min"}
	topics := make([]string, 0, len(categories)*len(windows))
	for _, category := range categories {
		for _, window := range windows {
			topics = append(topics, "$SYS/broker/load/"+category+"/"+window)
		}
	}
	return topics
}()

func TestResolve_knownTopics(t *testing.T) {
	t.Parallel()
	if len(topicCases) != 31 {
		t.Fatalf("topicCases has %d entries, want 31 (see DESIGN.md §6.1/§6.2/§6.3)", len(topicCases))
	}
	for _, tt := range topicCases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := Resolve(tt.topic, tt.payload)
			if reason != OK {
				t.Fatalf("Resolve(%q, %q) reason = %v, want OK", tt.topic, tt.payload, reason)
			}
			if got.Name != tt.want.Name {
				t.Errorf("Resolve(%q, ...) Name = %q, want %q", tt.topic, got.Name, tt.want.Name)
			}
			if got.Kind != tt.want.Kind {
				t.Errorf("Resolve(%q, ...) Kind = %v, want %v", tt.topic, got.Kind, tt.want.Kind)
			}
			if got.Value != tt.want.Value {
				t.Errorf("Resolve(%q, ...) Value = %v, want %v", tt.topic, got.Value, tt.want.Value)
			}
			if !maps.Equal(got.Labels, tt.want.Labels) {
				t.Errorf("Resolve(%q, ...) Labels = %v, want %v", tt.topic, got.Labels, tt.want.Labels)
			}
		})
	}
}

func TestResolve_droppedLoadSeries(t *testing.T) {
	t.Parallel()
	if len(droppedLoadTopics) != 24 {
		t.Fatalf("droppedLoadTopics has %d entries, want 24 (8 categories x 3 windows, §6.3)", len(droppedLoadTopics))
	}
	for _, topic := range droppedLoadTopics {
		t.Run(topic, func(t *testing.T) {
			t.Parallel()
			got, reason := Resolve(topic, "42")
			// Excluded, not NotExported: these are recognised and dropped on
			// purpose (§6.3). Conflating them with unknown topics makes the
			// unknown-topic counter climb by 24 every sys_interval on a
			// healthy broker and buries the signal it exists to give.
			if reason != Excluded {
				t.Errorf("Resolve(%q, ...) reason = %v, want Excluded", topic, reason)
			}
			if !reflect.DeepEqual(got, Sample{}) {
				t.Errorf("Resolve(%q, ...) = %+v, want zero Sample", topic, got)
			}
		})
	}
}

func TestResolve_unknownTopics(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		topic string
	}{
		{"removed in 2018 (1.5)", "$SYS/broker/timestamp"},
		{"documented but never published (mqtt.8 bug)", "$SYS/broker/mqtt/publish/received"},
		{"log topic is never a metric", "$SYS/broker/log/N"},
		{"made up topic", "$SYS/broker/does/not/exist"},
		{"future load topic is unknown", "$SYS/broker/load/new/family/1min"},
		{"known load family with future window is unknown", "$SYS/broker/load/messages/received/30min"},
		{"not a $SYS topic at all", "some/other/topic"},
		{"empty topic", ""},
		// The bridge prefix/suffix would overlap on their shared "/" if the
		// slice bounds check did not run first - this must not panic.
		{"bridge topic with no client id", "$SYS/broker/connection/state"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := Resolve(tt.topic, "42")
			if reason != NotExported {
				t.Errorf("Resolve(%q, ...) reason = %v, want NotExported", tt.topic, reason)
			}
			if !reflect.DeepEqual(got, Sample{}) {
				t.Errorf("Resolve(%q, ...) = %+v, want zero Sample", tt.topic, got)
			}
		})
	}
}

// TestResolve_aliasCollapse proves each deprecated pair yields exactly one
// series (§6.3): both topics resolve to the same Name, not merely to the
// same hardcoded string in topicCases above.
func TestResolve_aliasCollapse(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name             string
		canonical, alias string
	}{
		{"clients/connected <-> clients/active", "$SYS/broker/clients/connected", "$SYS/broker/clients/active"},
		{"clients/disconnected <-> clients/inactive", "$SYS/broker/clients/disconnected", "$SYS/broker/clients/inactive"},
		{"messages/stored <-> store/messages/count", "$SYS/broker/messages/stored", "$SYS/broker/store/messages/count"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			canonical, canonicalReason := Resolve(tt.canonical, "7")
			alias, aliasReason := Resolve(tt.alias, "7")
			if canonicalReason != OK || aliasReason != OK {
				t.Fatalf("reasons = (%v, %v), want (OK, OK)", canonicalReason, aliasReason)
			}
			if canonical.Name != alias.Name {
				t.Errorf("canonical Name = %q, alias Name = %q, want equal", canonical.Name, alias.Name)
			}
			if canonical.Kind != alias.Kind {
				t.Errorf("canonical Kind = %v, alias Kind = %v, want equal", canonical.Kind, alias.Kind)
			}
		})
	}
}

func TestResolve_version(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name       string
		payload    string
		wantReason Reason
		wantVer    string
	}{
		{"normal payload", "mosquitto version 2.1.2", OK, "2.1.2"},
		// Must never be routed through the numeric parser: a naive float
		// extraction would stop at "2.1" and silently drop ".2".
		{"payload never parsed as a float", "mosquitto version 2.1.2", OK, "2.1.2"},
		{"empty payload", "", InvalidPayload, ""},
		{"whitespace only payload", "   ", InvalidPayload, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, reason := Resolve("$SYS/broker/version", tt.payload)
			if reason != tt.wantReason {
				t.Fatalf("reason = %v, want %v", reason, tt.wantReason)
			}
			if reason != OK {
				return
			}
			if got.Name != "mosquitto_broker_info" {
				t.Errorf("Name = %q, want mosquitto_broker_info", got.Name)
			}
			if got.Kind != Gauge {
				t.Errorf("Kind = %v, want Gauge", got.Kind)
			}
			if got.Value != 1 {
				t.Errorf("Value = %v, want 1", got.Value)
			}
			if got.Labels["mosquitto_version"] != tt.wantVer {
				t.Errorf("mosquitto_version label = %q, want %q", got.Labels["mosquitto_version"], tt.wantVer)
			}
		})
	}
}

func TestResolve_bridge(t *testing.T) {
	t.Parallel()
	t.Run("connected", func(t *testing.T) {
		t.Parallel()
		got, reason := Resolve("$SYS/broker/connection/broker-b/state", "1")
		if reason != OK {
			t.Fatalf("reason = %v, want OK", reason)
		}
		if got.Name != "mosquitto_broker_bridge_connected" {
			t.Errorf("Name = %q, want mosquitto_broker_bridge_connected", got.Name)
		}
		if got.Value != 1 {
			t.Errorf("Value = %v, want 1", got.Value)
		}
		if got.Labels["mosquitto_bridge_name"] != "broker-b" {
			t.Errorf("mosquitto_bridge_name label = %q, want %q", got.Labels["mosquitto_bridge_name"], "broker-b")
		}
	})

	t.Run("disconnected", func(t *testing.T) {
		t.Parallel()
		got, reason := Resolve("$SYS/broker/connection/broker-b/state", "0")
		if reason != OK {
			t.Fatalf("reason = %v, want OK", reason)
		}
		if got.Value != 0 {
			t.Errorf("Value = %v, want 0", got.Value)
		}
	})

	t.Run("client id containing a slash goes entirely into the label, not the name", func(t *testing.T) {
		t.Parallel()
		got, reason := Resolve("$SYS/broker/connection/site-a/bridge-1/state", "1")
		if reason != OK {
			t.Fatalf("reason = %v, want OK", reason)
		}
		if got.Name != "mosquitto_broker_bridge_connected" {
			t.Errorf("Name = %q, want mosquitto_broker_bridge_connected (client id must never reach the name)", got.Name)
		}
		if got.Labels["mosquitto_bridge_name"] != "site-a/bridge-1" {
			t.Errorf("mosquitto_bridge_name label = %q, want %q", got.Labels["mosquitto_bridge_name"], "site-a/bridge-1")
		}
	})

	t.Run("invalid state payload", func(t *testing.T) {
		t.Parallel()
		_, reason := Resolve("$SYS/broker/connection/broker-b/state", "maybe")
		if reason != InvalidPayload {
			t.Errorf("reason = %v, want InvalidPayload", reason)
		}
	})

	t.Run("non-UTF-8 client id is sanitised, not trusted verbatim", func(t *testing.T) {
		t.Parallel()
		topic := bridgeTopicPrefix + "\xff\xfe" + bridgeTopicSuffix
		got, reason := Resolve(topic, "1")
		if reason != OK {
			t.Fatalf("reason = %v, want OK", reason)
		}
		name := got.Labels["mosquitto_bridge_name"]
		if name == "" {
			t.Error("mosquitto_bridge_name label is empty, want a sanitised replacement")
		}
		if !utf8.ValidString(name) {
			t.Errorf("mosquitto_bridge_name label %q is not valid UTF-8", name)
		}
	})
}

func TestResolve_invalidPayload(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{"", "garbage", "not a number"} {
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			got, reason := Resolve("$SYS/broker/bytes/received", payload)
			if reason != InvalidPayload {
				t.Errorf("reason = %v, want InvalidPayload", reason)
			}
			if !reflect.DeepEqual(got, Sample{}) {
				t.Errorf("Sample = %+v, want zero Sample", got)
			}
		})
	}
}

// TestResolve_validNames proves every Name and label key this package can
// produce would actually render as a Prometheus/OTel series: G1 requires
// this, not just eyeballing the table above.
func TestResolve_validNames(t *testing.T) {
	t.Parallel()
	samples := make([]Sample, 0, len(topicCases)+2)
	for _, tt := range topicCases {
		got, reason := Resolve(tt.topic, tt.payload)
		if reason != OK {
			t.Fatalf("Resolve(%q, ...) reason = %v, want OK", tt.topic, reason)
		}
		samples = append(samples, got)
	}
	version, reason := Resolve("$SYS/broker/version", "mosquitto version 2.1.2")
	if reason != OK {
		t.Fatalf("version reason = %v, want OK", reason)
	}
	samples = append(samples, version)
	bridge, reason := Resolve("$SYS/broker/connection/broker-b/state", "1")
	if reason != OK {
		t.Fatalf("bridge reason = %v, want OK", reason)
	}
	samples = append(samples, bridge)

	for _, s := range samples {
		if !validMetricName.MatchString(s.Name) {
			t.Errorf("Name %q is not a valid Prometheus metric name", s.Name)
		}
		for key := range s.Labels {
			if !validLabelKey.MatchString(key) {
				t.Errorf("label key %q (on %q) is not a valid Prometheus label name", key, s.Name)
			}
		}
	}
}

// TestParseValue pins the numeric extraction every non-special topic in the
// table above relies on.
func TestParseValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   string
		wantValue float64
		wantOK    bool
	}{
		{name: "trailing unit text", payload: "1234 seconds", wantValue: 1234, wantOK: true},
		{name: "zero with decimals", payload: "0.00", wantValue: 0, wantOK: true},
		{name: "negative integer keeps its sign", payload: "-5", wantValue: -5, wantOK: true},
		{name: "negative decimal keeps its sign", payload: "-5.5", wantValue: -5.5, wantOK: true},
		{name: "empty string has no sample", payload: "", wantValue: 0, wantOK: false},
		{name: "non-numeric garbage has no sample", payload: "garbage", wantValue: 0, wantOK: false},
		{name: "huge value overflows float64", payload: strings.Repeat("9", 400), wantValue: 0, wantOK: false},
		{name: "huge but in-range value parses fine", payload: strings.Repeat("9", 20), wantValue: 1e+20, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotOK := parseValue(tt.payload)
			if gotValue != tt.wantValue || gotOK != tt.wantOK {
				t.Errorf("parseValue(%q) = (%v, %v), want (%v, %v)", tt.payload, gotValue, gotOK, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func FuzzParsePayload(f *testing.F) {
	for _, seed := range []string{"", "0", "-5.5", "1234 seconds", "mosquitto version 2.1.2"} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, payload string) {
		_, _ = parseValue(payload)
	})
}
