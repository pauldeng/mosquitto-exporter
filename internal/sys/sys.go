// Package sys resolves a raw $SYS topic and payload into the Prometheus
// series it exports. All topic knowledge (the static 2.1.2 inventory, the
// final v1.0.0 names from DESIGN.md §6, the alias collapses and the dropped
// load/** series) and all payload parsing live here, behind Resolve. Pure
// functions only: no I/O, no globals mutated at runtime, no clock.
package sys

import (
	"regexp"
	"strconv"
	"strings"
)

// Kind is the Prometheus/OTel metric type a resolved Sample is rendered as.
type Kind uint8

const (
	Counter Kind = iota + 1
	Gauge
)

// Sample is one resolved, ready-to-export series.
type Sample struct {
	Name   string            // final Prometheus name, e.g. "mosquitto_broker_bytes_received_total"
	Kind   Kind              // meaningless when Resolve's Reason is not OK
	Help   string            // meaningless when Resolve's Reason is not OK
	Labels map[string]string // nil when the series has none
	Value  float64           // meaningless when Resolve's Reason is not OK
}

// Reason explains why Resolve did or did not produce a Sample.
type Reason uint8

const (
	OK             Reason = iota
	NotExported           // unrecognised topic: not in the static table
	InvalidPayload        // topic is known but the payload did not parse
	Excluded              // recognised, but deliberately not exported (§6.3)
)

// topicSpec is the static, versioned mapping for one $SYS topic. Two topic
// keys sharing the same name/kind/help collapse a deprecated alias onto its
// canonical series (DESIGN.md §6.3); labels is nil except for the three
// load/connections/* entries, which share one name distinguished by window.
type topicSpec struct {
	name   string
	kind   Kind
	help   string
	labels map[string]string
}

// topics is the complete, static export table: the verified 2.1.2 $SYS
// inventory (DESIGN.md §4), renamed per §6.1/§6.2, reclassified per the five
// §3.4 fixes, with the three deprecated aliases collapsed 2→1 and only
// load/connections/** kept out of the 27 load/** series (§6.3). version and
// bridge connection-state topics are handled outside this map (see Resolve).
// A topic that is in none of these is unknown and returns NotExported —
// there is no dynamic discovery and no gauge-by-default fallback.
var topics = map[string]topicSpec{
	// --- Counters (§6.1) ---
	"$SYS/broker/messages/received": {
		name: "mosquitto_broker_messages_received_total", kind: Counter,
		help: "The total number of messages of any type received since the broker started.",
	},
	"$SYS/broker/messages/sent": {
		name: "mosquitto_broker_messages_sent_total", kind: Counter,
		help: "The total number of messages of any type sent since the broker started.",
	},
	"$SYS/broker/bytes/received": {
		name: "mosquitto_broker_bytes_received_total", kind: Counter,
		help: "The total number of bytes received since the broker started.",
	},
	"$SYS/broker/bytes/sent": {
		name: "mosquitto_broker_bytes_sent_total", kind: Counter,
		help: "The total number of bytes sent since the broker started.",
	},
	"$SYS/broker/publish/bytes/received": {
		name: "mosquitto_broker_publish_bytes_received_total", kind: Counter,
		help: "The total number of bytes received in PUBLISH messages since the broker started.",
	},
	"$SYS/broker/publish/bytes/sent": {
		name: "mosquitto_broker_publish_bytes_sent_total", kind: Counter,
		help: "The total number of bytes sent in PUBLISH messages since the broker started.",
	},
	"$SYS/broker/publish/messages/received": {
		name: "mosquitto_broker_publish_messages_received_total", kind: Counter,
		help: "The total number of PUBLISH messages received since the broker started.",
	},
	"$SYS/broker/publish/messages/sent": {
		name: "mosquitto_broker_publish_messages_sent_total", kind: Counter,
		help: "The total number of PUBLISH messages sent since the broker started.",
	},
	"$SYS/broker/publish/messages/dropped": {
		name: "mosquitto_broker_publish_messages_dropped_total", kind: Counter,
		help: "The total number of PUBLISH messages dropped due to inflight/queuing limits since the broker started.",
	},
	// Fix (§3.4): real counter, was exported as a Gauge.
	"$SYS/broker/connections/socket/count": {
		name: "mosquitto_broker_connections_socket_total", kind: Counter,
		help: "The total number of socket connections opened since the broker started.",
	},
	// Fix (§3.4): real counter, was exported as a Gauge.
	"$SYS/broker/clients/expired": {
		name: "mosquitto_broker_clients_expired_total", kind: Counter,
		help: "The total number of disconnected persistent clients that have been expired and removed since the broker started.",
	},

	// --- Gauges (§6.2) ---
	// Fix (§3.4): HASH_CNT high-water/current count, was exported as a Counter.
	"$SYS/broker/clients/total": {
		name: "mosquitto_broker_clients_total", kind: Gauge,
		help: "The number of currently connected and disconnected clients with a persistent session registered on the broker.",
	},
	"$SYS/broker/clients/connected": {
		name: "mosquitto_broker_clients_connected", kind: Gauge,
		help: "The number of currently connected clients.",
	},
	// Deprecated alias of clients/connected, collapsed 2→1 (§6.3).
	"$SYS/broker/clients/active": {
		name: "mosquitto_broker_clients_connected", kind: Gauge,
		help: "The number of currently connected clients.",
	},
	"$SYS/broker/clients/disconnected": {
		name: "mosquitto_broker_clients_disconnected", kind: Gauge,
		help: "The number of currently disconnected clients with a persistent session registered on the broker.",
	},
	// Deprecated alias of clients/disconnected, collapsed 2→1 (§6.3).
	"$SYS/broker/clients/inactive": {
		name: "mosquitto_broker_clients_disconnected", kind: Gauge,
		help: "The number of currently disconnected clients with a persistent session registered on the broker.",
	},
	// Fix (§3.4): running high-water mark, was exported as a Counter.
	"$SYS/broker/clients/maximum": {
		name: "mosquitto_broker_clients_maximum", kind: Gauge,
		help: "The maximum number of clients that have been connected to the broker at the same time.",
	},
	"$SYS/broker/messages/stored": {
		name: "mosquitto_broker_messages_stored", kind: Gauge,
		help: "The number of messages currently held in the message store.",
	},
	// Duplicate of messages/stored, collapsed 2→1 (§3.4, §6.3).
	"$SYS/broker/store/messages/count": {
		name: "mosquitto_broker_messages_stored", kind: Gauge,
		help: "The number of messages currently held in the message store.",
	},
	"$SYS/broker/store/messages/bytes": {
		name: "mosquitto_broker_store_messages_bytes", kind: Gauge,
		help: "The number of bytes currently used to store messages.",
	},
	"$SYS/broker/subscriptions/count": {
		name: "mosquitto_broker_subscriptions", kind: Gauge,
		help: "The total number of subscriptions active on the broker.",
	},
	"$SYS/broker/shared_subscriptions/count": {
		name: "mosquitto_broker_shared_subscriptions", kind: Gauge,
		help: "The total number of shared subscriptions active on the broker.",
	},
	// Real topic has a literal space ("retained messages"), not an underscore.
	"$SYS/broker/retained messages/count": {
		name: "mosquitto_broker_retained_messages", kind: Gauge,
		help: "The total number of retained messages active on the broker.",
	},
	"$SYS/broker/heap/current": {
		name: "mosquitto_broker_heap_current_bytes", kind: Gauge,
		help: "The current size of the heap memory in use by the broker, in bytes.",
	},
	"$SYS/broker/heap/maximum": {
		name: "mosquitto_broker_heap_maximum_bytes", kind: Gauge,
		help: "The largest amount of heap memory used by the broker, in bytes.",
	},
	"$SYS/broker/packet/out/count": {
		name: "mosquitto_broker_packet_out_count", kind: Gauge,
		help: "The number of packets pending output to the network.",
	},
	"$SYS/broker/packet/out/bytes": {
		name: "mosquitto_broker_packet_out_bytes", kind: Gauge,
		help: "The number of bytes pending output to the network.",
	},
	// Fix (§3.4): monotonically-reporting wall clock, was exported as a Counter.
	"$SYS/broker/uptime": {
		name: "mosquitto_broker_uptime_seconds", kind: Gauge,
		help: "The number of seconds since the broker started.",
	},

	// --- load/** shape change (§6.3): 27 series -> 3, one name + a window label.
	// The other 8 categories (24 series) are pure EWMAs of counters already
	// exported above and are deliberately absent from this table, so they
	// fall through Resolve to NotExported.
	"$SYS/broker/load/connections/1min": {
		name: "mosquitto_broker_load_connections", kind: Gauge,
		help:   "The moving average of the number of CONNECT packets received by the broker.",
		labels: map[string]string{"window": "1m"},
	},
	"$SYS/broker/load/connections/5min": {
		name: "mosquitto_broker_load_connections", kind: Gauge,
		help:   "The moving average of the number of CONNECT packets received by the broker.",
		labels: map[string]string{"window": "5m"},
	},
	"$SYS/broker/load/connections/15min": {
		name: "mosquitto_broker_load_connections", kind: Gauge,
		help:   "The moving average of the number of CONNECT packets received by the broker.",
		labels: map[string]string{"window": "15m"},
	},
}

// topicVersion is handled outside the table: its payload is a version
// string, not a number, and it renders as an info-metric (§6.2).
const topicVersion = "$SYS/broker/version"

// Bridge connection state topics carry the admin-controlled remote_clientid
// in the middle of the topic, so they cannot be a static map key. Sliced by
// fixed prefix/suffix length rather than a regexp or a Split, so a client id
// containing "/" is handled correctly without ambiguity (§6.3 bridge case).
const (
	bridgeTopicPrefix = "$SYS/broker/connection/"
	bridgeTopicSuffix = "/state"
)

// isExcludedLoadSeries reports whether topic is one of the 24 load/** series
// dropped by §6.3. They are pure EWMAs of counters we already export, so a
// backend computing its own rate() gains nothing from them — but they are
// recognised, not unknown.
func isExcludedLoadSeries(topic string) bool {
	const prefix = "$SYS/broker/load/"
	rest, ok := strings.CutPrefix(topic, prefix)
	if !ok {
		return false
	}
	i := strings.LastIndexByte(rest, '/')
	if i < 1 {
		return false
	}
	family, window := rest[:i], rest[i+1:]
	if window != "1min" && window != "5min" && window != "15min" {
		return false
	}
	switch family {
	case "messages/received", "messages/sent", "publish/dropped", "publish/received",
		"publish/sent", "bytes/received", "bytes/sent", "sockets":
		return true
	default:
		return false
	}
}

// Resolve maps a raw $SYS topic and payload to the series to export.
func Resolve(topic, payload string) (Sample, Reason) {
	if id, ok := bridgeClientID(topic); ok {
		return resolveBridge(id, payload)
	}
	if topic == topicVersion {
		return resolveVersion(payload)
	}

	spec, ok := topics[topic]
	if !ok {
		// Distinguish "we deliberately drop this" from "we do not recognise
		// this". Both go unexported, but only the latter is worth alerting
		// on. Folding them together makes the unknown-topic counter climb by
		// 24 every sys_interval on a perfectly healthy broker, which buries
		// the one signal it exists to give (§5.4).
		if isExcludedLoadSeries(topic) {
			return Sample{}, Excluded
		}
		return Sample{}, NotExported
	}
	value, ok := parseValue(payload)
	if !ok {
		return Sample{}, InvalidPayload
	}
	return Sample{Name: spec.name, Kind: spec.kind, Help: spec.help, Labels: spec.labels, Value: value}, OK
}

// resolveVersion handles $SYS/broker/version. The payload is a string like
// "mosquitto version 2.1.2" — it must never be parsed as a float, only its
// trailing version token extracted and carried as a label on a fixed value.
func resolveVersion(payload string) (Sample, Reason) {
	fields := strings.Fields(payload)
	if len(fields) == 0 {
		return Sample{}, InvalidPayload
	}
	version := fields[len(fields)-1]
	return Sample{
		Name:   "mosquitto_broker_info",
		Kind:   Gauge,
		Help:   "Information about the running broker; the sample value is always 1.",
		Labels: map[string]string{"mosquitto_version": version},
		Value:  1,
	}, OK
}

// bridgeClientID reports the remote_clientid embedded in a bridge connection
// state topic. The length check comes first so the slice below can never go
// out of range, and it also rejects the degenerate topic
// "$SYS/broker/connection/state" (no id at all), where the prefix's trailing
// "/" and the suffix's leading "/" would otherwise be the same character.
func bridgeClientID(topic string) (string, bool) {
	if len(topic) < len(bridgeTopicPrefix)+len(bridgeTopicSuffix) {
		return "", false
	}
	if !strings.HasPrefix(topic, bridgeTopicPrefix) || !strings.HasSuffix(topic, bridgeTopicSuffix) {
		return "", false
	}
	id := topic[len(bridgeTopicPrefix) : len(topic)-len(bridgeTopicSuffix)]
	if id == "" {
		return "", false
	}
	return id, true
}

// resolveBridge handles $SYS/broker/connection/<remote_clientid>/state. The
// payload is the literal character '0' or '1'; anything else is an unparsed
// payload on a known topic.
func resolveBridge(id, payload string) (Sample, Reason) {
	var value float64
	switch strings.TrimSpace(payload) {
	case "0":
		value = 0
	case "1":
		value = 1
	default:
		return Sample{}, InvalidPayload
	}

	name, ok := sanitizeLabelValue(id)
	if !ok {
		return Sample{}, NotExported
	}
	return Sample{
		Name:   "mosquitto_broker_bridge_connected",
		Kind:   Gauge,
		Help:   "Whether this broker's connection to a bridge is currently established (1) or not (0).",
		Labels: map[string]string{"mosquitto_bridge_name": name},
		Value:  value,
	}, OK
}

// sanitizeLabelValue makes an admin-controlled remote_clientid safe to carry
// as a Prometheus/OTel label value. Label values are otherwise permissive —
// there is no character to escape here — but a value that is not valid UTF-8
// cannot be represented as a Go string attribute at all, so invalid byte
// sequences are replaced rather than trusted verbatim. ok is false only if
// nothing usable remains, which id being non-empty on entry makes
// unreachable in practice; kept as a defensive boundary check rather than an
// assumption.
func sanitizeLabelValue(id string) (string, bool) {
	clean := strings.ToValidUTF8(id, "\uFFFD")
	if clean == "" {
		return "", false
	}
	return clean, true
}

// validValue matches the first optionally-signed integer or decimal number
// in a payload. Package-level so the pattern compiles once, not on every
// message.
var validValue = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// parseValue extracts the first number in payload. ok is false when no
// number is found, or it does not fit in a float64: callers must not record
// a sample in that case, since 0 is a valid real value and has to stay
// distinguishable from "failed to parse".
func parseValue(payload string) (float64, bool) {
	match := validValue.FindString(payload)
	if match == "" {
		return 0, false
	}
	value, err := strconv.ParseFloat(match, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}
