package sys

import (
	"strings"
	"testing"
)

// These are the highest-value tests in the project: TopicName, ClassifyTopic
// and ParseValue are pure functions over broker-controlled input, and every
// exported metric name flows through them. DESIGN.md 5.7 puts them here and
// 8 G2 sets this package's coverage floor at 90%.

func TestClassifyTopic(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		topic string
		want  Kind
		ok    bool
	}{
		{name: "counter topic", topic: "$SYS/broker/bytes/received", want: Counter, ok: true},
		{name: "gauge topic", topic: "$SYS/broker/clients/connected", want: Gauge, ok: true},
		{name: "load EWMA is a gauge", topic: "$SYS/broker/load/messages/received/1min", want: Gauge, ok: true},
		{name: "static version string is ignored", topic: "$SYS/broker/version", ok: false},
		{name: "build timestamp is ignored", topic: "$SYS/broker/timestamp", ok: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ClassifyTopic(tt.topic)
			if ok != tt.ok {
				t.Fatalf("ClassifyTopic(%q) ok = %v, want %v", tt.topic, ok, tt.ok)
			}
			if ok && got != tt.want {
				t.Errorf("ClassifyTopic(%q) = %v, want %v", tt.topic, got, tt.want)
			}
		})
	}
}

func TestTopicName(t *testing.T) {
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
			if got := TopicName(tt.topic); got != tt.want {
				t.Errorf("TopicName(%q) = %q, want %q", tt.topic, got, tt.want)
			}
		})
	}
}

// TestParseValue pins parseValue's output. Phase 1 changed its signature to
// (float64, bool) and fixed C5 (parse failure is no longer indistinguishable
// from a genuine zero) and C6 (a negative integer no longer loses its sign).
func TestParseValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		payload   string
		wantValue float64
		wantOK    bool
	}{
		{name: "trailing unit text", payload: "1234 seconds", wantValue: 1234, wantOK: true},
		// Picks the first "digits.digits" run and stops there - never notices
		// the second ".2". A pre-existing parsing quirk, not one of C1-C6: this
		// payload is never actually routed here in production because
		// "$SYS/broker/version" is in ignoreKeyMetrics.
		{name: "dotted version string", payload: "mosquitto version 2.1.2", wantValue: 2.1, wantOK: true},
		{name: "zero with decimals", payload: "0.00", wantValue: 0, wantOK: true},
		// C6 (fixed): the old regex's `-?` only applied to the decimal
		// alternative (`-?\d+\.\d+`), not the bare-integer alternative
		// (`\d+`), so a negative integer lost its sign while a negative
		// decimal kept it. Both now keep their sign.
		{name: "negative integer keeps its sign (C6 fixed)", payload: "-5", wantValue: -5, wantOK: true},
		{name: "negative decimal keeps its sign", payload: "-5.5", wantValue: -5.5, wantOK: true},
		// C5 (fixed): parse failure now reports ok=false instead of a silent 0.
		{name: "empty string (C5 fixed: no sample)", payload: "", wantValue: 0, wantOK: false},
		{name: "non-numeric garbage (C5 fixed: no sample)", payload: "garbage", wantValue: 0, wantOK: false},
		// C5 via a second path: strconv.ParseFloat's range error on an
		// out-of-float64-range literal is now also reported as ok=false.
		{name: "huge value overflows float64 (C5 fixed: no sample)", payload: strings.Repeat("9", 400), wantValue: 0, wantOK: false},
		{name: "huge but in-range value parses fine", payload: strings.Repeat("9", 20), wantValue: 1e+20, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotValue, gotOK := ParseValue(tt.payload)
			if gotValue != tt.wantValue || gotOK != tt.wantOK {
				t.Errorf("ParseValue(%q) = (%v, %v), want (%v, %v)", tt.payload, gotValue, gotOK, tt.wantValue, tt.wantOK)
			}
		})
	}
}

func FuzzParsePayload(f *testing.F) {
	for _, seed := range []string{"", "0", "-5.5", "1234 seconds", "mosquitto version 2.1.2"} {
		f.Add(seed)
	}
	f.Fuzz(func(_ *testing.T, payload string) {
		_, _ = ParseValue(payload)
	})
}
