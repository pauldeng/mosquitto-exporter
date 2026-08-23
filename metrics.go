package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	ignoreKeyMetrics = map[string]string{
		"$SYS/broker/timestamp":        "The timestamp at which this particular build of the broker was made. Static.",
		"$SYS/broker/version":          "The version of the broker. Static.",
		"$SYS/broker/clients/active":   "deprecated in favour of $SYS/broker/clients/connected",
		"$SYS/broker/clients/inactive": "deprecated in favour of $SYS/broker/clients/disconnected",
	}
	counterKeyMetrics = map[string]string{
		"$SYS/broker/bytes/received":            "The total number of bytes received since the broker started.",
		"$SYS/broker/bytes/sent":                "The total number of bytes sent since the broker started.",
		"$SYS/broker/messages/received":         "The total number of messages of any type received since the broker started.",
		"$SYS/broker/messages/sent":             "The total number of messages of any type sent since the broker started.",
		"$SYS/broker/publish/bytes/received":    "The total number of PUBLISH bytes received since the broker started.",
		"$SYS/broker/publish/bytes/sent":        "The total number of PUBLISH bytes sent since the broker started.",
		"$SYS/broker/publish/messages/received": "The total number of PUBLISH messages received since the broker started.",
		"$SYS/broker/publish/messages/sent":     "The total number of PUBLISH messages sent since the broker started.",
		"$SYS/broker/publish/messages/dropped":  "The total number of PUBLISH messages that have been dropped due to inflight/queuing limits.",
		"$SYS/broker/uptime":                    "The total number of seconds since the broker started.",
		"$SYS/broker/clients/maximum":           "The maximum number of clients connected simultaneously since the broker started",
		"$SYS/broker/clients/total":             "The total number of clients connected since the broker started.",
	}
)

// metricKind is the Prometheus metric type a topic is rendered as. Phase 1
// keeps today's classification exactly (DESIGN.md §7 Phase 1 item 1); the
// C-row reclassification (§3.4) is Phase 3 work.
type metricKind uint8

const (
	kindGauge metricKind = iota
	kindCounter
)

// classifyTopic reports how topic should be recorded: kind is meaningless
// when ok is false. Ignored topics (deprecated aliases, static build-time
// strings) produce no metric at all, exactly as before.
func classifyTopic(topic string) (kind metricKind, ok bool) {
	if _, ignored := ignoreKeyMetrics[topic]; ignored {
		return 0, false
	}
	if _, isCounter := counterKeyMetrics[topic]; isCounter {
		return kindCounter, true
	}
	return kindGauge, true
}

// topicReplacer converts a $SYS topic into a Prometheus-safe metric name.
// Hoisted to package level (P2, DESIGN.md §3.5): previously rebuilt via
// four sequential strings.Replace(All) calls every time a topic was first
// registered.
var topicReplacer = strings.NewReplacer(
	"/", "_",
	" ", "_",
	"-", "_",
	".", "_",
)

func parseTopic(topic string) string {
	name := strings.Replace(topic, "$SYS/", "", 1)
	return topicReplacer.Replace(name)
}

// validValue matches the first optionally-signed integer or decimal number
// in a payload. Hoisted to package level (P1, DESIGN.md §3.5): previously
// regexp.MustCompile ran on every single message via parseValue.
//
// Fixes C6: the old pattern `-?\d{1,}[.]\d{1,}|\d{1,}` bound `-?` only to
// the decimal alternative, so "-5" parsed as 5 while "-5.5" kept its sign.
// Moving `-?` outside the alternation applies it to both forms.
var validValue = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

// parseValue extracts the first number in payload. ok is false when no
// number is found, or it does not fit in a float64 (C5): callers must not
// record a sample in that case, since 0 is a valid real value and has to
// stay distinguishable from "failed to parse".
//
// "mosquitto version 2.1.2" -> (2.1, true): the regex stops at the first
// dot-delimited run and never notices the second ".2". This is a pre-existing
// parsing quirk, not one of C1-C6, and it is harmless in production because
// $SYS/broker/version is in ignoreKeyMetrics and never reaches this function.
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
