# Shipping mosquitto.log to Loki

**This exporter does not tail files.** It holds an MQTT socket to the
broker's `$SYS` tree and has no code path that opens, watches, or parses
`mosquitto.log` (DESIGN.md §5.6). Log rotation detection, inode tracking,
restart checkpointing, multiline assembly, backpressure and retry are a
solved problem, already battle-tested in the OpenTelemetry Collector's
`filelog` receiver and in Grafana Alloy's `loki.source.file` - reimplementing
that in Go here would just be a worse copy of one of them. `otelcol-contrib`
has no MQTT or Mosquitto receiver (checked directly against its component
list), so this is a second, independent process/sidecar next to the
exporter, not a feature the exporter grows.

This directory ships two ready-to-run, equivalent pipelines:

- [`../deploy/otelcol-contrib.yaml`](../deploy/otelcol-contrib.yaml) - OpenTelemetry Collector Contrib
- [`../deploy/alloy.river`](../deploy/alloy.river) - Grafana Alloy

Pick one, not both.

## Which one to run

**If you already run one of them, use that one** - don't add a second log
agent to a host that already has a collector for something else. Otherwise:

- **Grafana Alloy** if you're already on the Grafana stack (Loki, Mimir,
  Tempo) - it's Loki-native (`loki.source.file` → `loki.process` →
  `loki.write`), the River config is short, and there's nothing to translate.
- **otelcol-contrib** if you're standardizing on OpenTelemetry end to end,
  or already run a collector for traces/other logs and want mosquitto's logs
  in the same pipeline. It reaches Loki over the OTLP logs endpoint
  (`otlphttp` exporter to `/otlp/v1/logs`) - Loki 3.0+ ingests OTLP natively.
  As of collector-contrib v0.159.0 there is no standalone `loki` exporter any
  more (checked against its `components` output); OTLP is the current,
  maintained path, not a workaround.

Both configs use the same file, `<epoch>: <message>` parse, and severity rule.
Loki exposes the correlation value as `instance` for Alloy and
`service_instance_id` for OTLP Collector logs.

## File vs. container stdout

Both configs are **file tailers** - they open `/var/log/mosquitto/mosquitto.log`
directly. That is the right tool only when something has a filesystem path to
that file: a bare-metal/systemd broker, or a container with a bind-mounted or
shared log volume.

**In plain Docker or Kubernetes, don't use `filelog`/`loki.source.file` on a
path at all.** The container runtime already captures the broker's stdout/
stderr - `docker logs`, the container's log driver, or (on Kubernetes) the
kubelet's own log collection and whatever your cluster already scrapes
(Alloy's `loki.source.kubernetes`, Promtail, the Loki Kubernetes-monitoring
Helm chart, a `fluent-bit` DaemonSet, etc.). Point mosquitto's `log_dest` at
`stdout` in that deployment shape, and wire its output into whatever log
pipeline your platform already runs instead of standing up a second one that
reads a file path the container doesn't reliably expose.

## Parsing: what mosquitto's log actually looks like

Default `log_dest` format is `<unix-epoch-seconds>: <message>` - **not**
ISO-8601. Verified directly against a live `eclipse-mosquitto:2.1.2-alpine`
container: most lines, including every connection-lifecycle line and
protocol-error line, carry **no** severity word at all:

```
1787486379: mosquitto version 2.1.2 starting
1787486381: New connection from ::1:39302 on port 1883.
1787486382: Client auto-C5A5C6FD-... [::1:39308] disconnected.
1787486419: Client auto-BBA1EA73-... [::1:39244] disconnected: not authorised.
1787486419: Protocol error from ::1:35925: First packet not CONNECT (67).
```

Only a minority of lines carry a real `Error:` / `Warning:` / `Notice:` /
`Information:` prefix, e.g.:

```
1787486441: Error: Unknown configuration variable 'totally_bogus_directive_xyz'.
1787486451: Error: Unable to open acl_file "/mosquitto/config/does_not_exist.acl".
1787486451: Error opening acl file "/mosquitto/config/does_not_exist.acl".
```

Note the last line: it contains the word "Error" but not as the `Error: `
prefix. Both pipelines match **only** that exact leading prefix - a
substring match on "error" anywhere in the message would also fire on
`Protocol error from ...` (a line mosquitto itself does not tag as an error)
and would be inventing a severity mosquitto never asserted. Verified with a
live collector run: that acl-file line, and the "Protocol error" line, both
ship with no severity label; the `Unknown configuration variable` line gets
`Error`.

**Unexpected line shapes are not rejected by the parser.** Both configs use
one expression whose timestamp and severity groups are optional and whose
body always matches. Alloy keeps the scrape timestamp when the epoch is
missing; Collector leaves the event timestamp unset and retains its observed
timestamp. This matters because log wording is not a stable interface.

## Setting `log_timestamp_format` (and its missing-offset trap)

Both configs assume the default epoch format. If you'd rather have
human-readable timestamps in the file itself, mosquitto's `log_timestamp_format`
takes a `strftime` format applied to **local time**, not UTC. Verified: the
man page's own suggested ISO-8601 example is:

```
log_timestamp_format %Y-%m-%dT%H:%M:%S
```

which carries **no timezone offset** - confirmed directly (a container
running in local time produced `2026-08-23T12:01:11:` with no `+00:00`
anywhere). A parser that assumes UTC will silently misread every timestamp
by your offset on any host not already running UTC. If you change this
setting at all, use:

```
log_timestamp_format %Y-%m-%dT%H:%M:%S%z
```

and update the `timestamp`/`stage.timestamp` operator in whichever config
you use to a real strptime layout instead of the epoch one shipped here
(`layout_type: strptime` / `layout: "%Y-%m-%dT%H:%M:%S%z"` for otelcol;
`format: "2006-01-02T15:04:05Z07:00"` for Alloy's Go-time-layout
`stage.timestamp`). Leaving the format on the epoch default and *not*
touching `log_timestamp_format` is simpler and is what both shipped configs
assume.

## `log_type`: what you see by default, and the cost of more

Default `log_type` is `error, warning, notice, information`. **`subscribe`,
`unsubscribe`, and `debug` are not logged unless you add them explicitly**:

```
log_type error
log_type warning
log_type notice
log_type information
log_type subscribe
log_type unsubscribe
```

Enabling `log_type debug` on top of that is a different order of magnitude,
not an incremental one - it logs a line for effectively every packet
(`Sending CONNACK to ...`, `Received PUBLISH from ...`, one line per PUBLISH
per subscriber on fan-out), verified directly against a live broker under
light test traffic. Treat it as a temporary diagnostic toggle, not something
to run permanently in front of either shipping pipeline - both will faithfully
ship every one of those lines to Loki, at whatever ingestion cost that implies
for your Loki tenant.

## Correlating logs with this exporter's metrics

Both configs attach a `service.instance.id` resource attribute (otelcol) /
`instance` label (Alloy) so you can join a broker's logs to this exporter's
metrics in Grafana. Per DESIGN.md §4.1, this exporter deliberately does not
invent or export its own identity label - target identity belongs in
scrape/collector config, and the same rule is kept here rather than adding a
special case for logs:

- Set the `MOSQUITTO_INSTANCE` environment variable the collector/Alloy
  process runs with to **whatever `instance` label your Prometheus scrape
  config already assigns** to this exporter's target (`relabel_configs` /
  `static_configs` labels), so a Grafana query can join
  `{job="mosquitto", instance="..."}` Alloy log lines against
  `mosquitto_broker_*{instance="..."}` metrics.
- If you also push this exporter's metrics over its OTLP leg
  (`--otlp-endpoint`, see [OTEL.md](OTEL.md)), set `MOSQUITTO_INSTANCE` to
  the same value as that leg's `OTEL_RESOURCE_ATTRIBUTES`'s
  `service.instance.id`, and both signals carry identical OTel resource
  identity.

For Collector logs, use
`{service_name="mosquitto", service_instance_id="..."}`; Loki's native OTLP
ingestion normalizes dots in resource-attribute names to underscores. Alloy
uses `{job="mosquitto", instance="..."}`. `otelcol-contrib validate` rejects
an unset `MOSQUITTO_INSTANCE`; Alloy accepts it but omits the empty label, so
set it explicitly whenever metrics/log correlation matters.

## Lower-fidelity alternative: `log_dest topic` (not implemented)

Mosquitto can republish its own log lines onto
`$SYS/broker/log/{I,N,W,E}` via `log_dest topic`, and this exporter already
holds an MQTT subscription to `$SYS/#` - so in principle it could pick these
up for free. **It does not today, and this phase does not add it.** It is
recorded here as an available broker feature and a possible future option:

- Useful specifically when the log *file* is unreachable - a remote broker,
  no shared volume, no sidecar filesystem access for a collector.
- Genuinely lower fidelity than `filelog`/`loki.source.file`: DEBUG-level
  messages are hard-excluded by the broker itself (a recursion guard, not a
  configurable choice), and `subscribe`/`unsubscribe` visibility still
  requires the same extra `log_type` values as above.
- Messages arrive QoS 2, not retained, with a 20-second expiry - there is no
  history to backfill from, only what's published while a subscriber is
  connected.

If this becomes worth having, it's a Phase 7 addition to the exporter itself
(an opt-in `$SYS/broker/log/#` → OTel log-record bridge), not a change to
either file in `deploy/`.

## Out of scope: auth/ACL logging

Neither pipeline attempts to extract authentication or ACL-denial signal
from these logs. DESIGN.md §2 covers why in detail: ACL denials are
DEBUG-only, and every auth failure collapses into one generic NOTICE line
with no username - log parsing cannot recover what the broker never logged.
