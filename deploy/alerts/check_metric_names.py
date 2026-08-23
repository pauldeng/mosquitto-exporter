#!/usr/bin/env python3
"""Check that dashboard and alert metric names exist in the implementation.

Stdlib only, deliberately: this repo pulls in a real tool (promtool, via
Docker) for rule *syntax*; this script only needs to grep two files for
metric-shaped identifiers, which does not earn a YAML-parser dependency.
"""

import json
import re
import sys
from pathlib import Path

STANDARD = {
    "process_resident_memory_bytes",
    "process_cpu_seconds_total",
    "up",
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


def implementation_names():
    source = Path("internal/sys/sys.go").read_text(encoding="utf-8")
    source += Path("internal/exporter/store.go").read_text(encoding="utf-8")
    return set(re.findall(r'"(mosquitto_[a-zA-Z0-9_]+)"', source)) | STANDARD


def main():
    found = names_in_alert_rules("deploy/alerts/mosquitto.rules.yaml")
    found |= names_in_dashboard("deploy/dashboards/mosquitto.json")

    unknown = sorted(found - implementation_names())
    if unknown:
        print("UNKNOWN / TYPO'D METRIC NAMES:", unknown)
        return 1
    print(f"OK: {len(found)} referenced metric names all match the implementation.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
