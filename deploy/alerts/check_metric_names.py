#!/usr/bin/env python3
"""CI check: every mosquitto_*/process_*/up metric referenced by the
Grafana dashboard or the Prometheus alert rules must be one of the names
DESIGN.md §6 actually defines (plus the exporter's own mosquitto_exporter_*
self-metrics and the client_golang process/go collectors preserved per
§6.4). Catches a typo'd metric name - the realistic failure mode here,
and one that is otherwise silent in Grafana.

Stdlib only, deliberately: this repo pulls in a real tool (promtool, via
Docker) for rule *syntax*; this script only needs to grep two files for
metric-shaped identifiers, which does not earn a YAML-parser dependency.
"""

import json
import re
import sys

KNOWN = {
    "mosquitto_broker_messages_received_total",
    "mosquitto_broker_messages_sent_total",
    "mosquitto_broker_bytes_received_total",
    "mosquitto_broker_bytes_sent_total",
    "mosquitto_broker_publish_bytes_received_total",
    "mosquitto_broker_publish_bytes_sent_total",
    "mosquitto_broker_publish_messages_received_total",
    "mosquitto_broker_publish_messages_sent_total",
    "mosquitto_broker_publish_messages_dropped_total",
    "mosquitto_broker_connections_socket_total",
    "mosquitto_broker_clients_expired_total",
    "mosquitto_broker_clients_total",
    "mosquitto_broker_clients_connected",
    "mosquitto_broker_clients_disconnected",
    "mosquitto_broker_clients_maximum",
    "mosquitto_broker_messages_stored",
    "mosquitto_broker_store_messages_bytes",
    "mosquitto_broker_subscriptions",
    "mosquitto_broker_shared_subscriptions",
    "mosquitto_broker_retained_messages",
    "mosquitto_broker_packet_out_count",
    "mosquitto_broker_packet_out_bytes",
    "mosquitto_broker_heap_current_bytes",
    "mosquitto_broker_heap_maximum_bytes",
    "mosquitto_broker_uptime_seconds",
    "mosquitto_broker_load_connections",
    "mosquitto_broker_info",
    "mosquitto_broker_bridge_connected",
    "mosquitto_up",
    "mosquitto_exporter_unknown_topics_total",
    "mosquitto_exporter_invalid_payloads_total",
    "mosquitto_exporter_dropped_topics_total",
    "process_resident_memory_bytes",
    "process_cpu_seconds_total",
    "up",  # standard Prometheus scrape-target health metric
}

NAME_RE = re.compile(r'\b(mosquitto_[a-zA-Z0-9_]+|process_[a-zA-Z0-9_]+|(?<![a-zA-Z0-9_"])up)\b')
EXPR_LINE_RE = re.compile(r'^\s*expr:\s*(.+?)\s*$')


def names_from_promql(expr):
    return set(NAME_RE.findall(expr))


def names_in_alert_rules(path):
    found = set()
    with open(path, encoding="utf-8") as f:
        for line in f:
            m = EXPR_LINE_RE.match(line)
            if m:
                found |= names_from_promql(m.group(1))
    return found


def names_in_dashboard(path):
    with open(path, encoding="utf-8") as f:
        dash = json.load(f)  # also validates the JSON parses
    found = set()
    for panel in dash.get("panels", []):
        for target in panel.get("targets", []):
            if "expr" in target:
                found |= names_from_promql(target["expr"])
    return found


def main():
    found = names_in_alert_rules("deploy/alerts/mosquitto.rules.yaml")
    found |= names_in_dashboard("deploy/dashboards/mosquitto.json")

    unknown = sorted(n for n in found if n not in KNOWN)
    if unknown:
        print("UNKNOWN / TYPO'D METRIC NAMES:", unknown)
        return 1
    print(f"OK: {len(found)} referenced metric names all match DESIGN.md §6 / self-metrics.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
