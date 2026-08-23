# Migrating to v1.0.0

v1.0.0 is a breaking release. It renames every metric this exporter
produces, fixes five wrong Prometheus types, collapses three duplicate
series, drops 24 redundant `load/**` series, changes one CLI default, and
removes one flag. See [`DESIGN.md`](../DESIGN.md) §6 for the full rationale
(not shipped in this repo checkout by default - ask whoever ran the
modernization for a copy if you need the "why", not just the "what").

This document is the "what to change" checklist. Read the whole thing before
upgrading a production instance: dashboards, alerts, and any `--reset-metrics`
or bare `--pass`/embedded-URL-credential usage all need action.

## 1. Metric name and type changes

Every metric is renamed. The table below is the complete mapping. "Type"
notes where a Prometheus type actually changed, not just the name - a type
change means `rate()`/`irate()` queries against the old name were producing
meaningless numbers and must not be ported forward unchanged.

### Counters

| New name | Old name | Type change |
|---|---|---|
| `mosquitto_broker_messages_received_total` | `broker_messages_received` | - |
| `mosquitto_broker_messages_sent_total` | `broker_messages_sent` | - |
| `mosquitto_broker_bytes_received_total` | `broker_bytes_received` | - |
| `mosquitto_broker_bytes_sent_total` | `broker_bytes_sent` | - |
| `mosquitto_broker_publish_bytes_received_total` | `broker_publish_bytes_received` | - |
| `mosquitto_broker_publish_bytes_sent_total` | `broker_publish_bytes_sent` | - |
| `mosquitto_broker_publish_messages_received_total` | `broker_publish_messages_received` | - |
| `mosquitto_broker_publish_messages_sent_total` | `broker_publish_messages_sent` | - |
| `mosquitto_broker_publish_messages_dropped_total` | `broker_publish_messages_dropped` | - |
| `mosquitto_broker_connections_socket_total` | `broker_connections_socket_count` | **Gauge -> Counter** (it always was a real counter) |
| `mosquitto_broker_clients_expired_total` | `broker_clients_expired` | **Gauge -> Counter** (it always was a real counter) |

### Gauges

| New name | Old name | Type change |
|---|---|---|
| `mosquitto_broker_clients_total` | `broker_clients_total` | **Counter -> Gauge** (`rate()` on the old series was already meaningless) |
| `mosquitto_broker_clients_connected` | `broker_clients_connected` | - |
| `mosquitto_broker_clients_disconnected` | `broker_clients_disconnected` | - |
| `mosquitto_broker_clients_maximum` | `broker_clients_maximum` | **Counter -> Gauge** (it is a high-water mark, `rate()` was already meaningless) |
| `mosquitto_broker_messages_stored` | `broker_messages_stored` **and** `broker_store_messages_count` | duplicate series collapsed 2 -> 1 |
| `mosquitto_broker_store_messages_bytes` | `broker_store_messages_bytes` | - |
| `mosquitto_broker_subscriptions` | `broker_subscriptions_count` | - |
| `mosquitto_broker_shared_subscriptions` | `broker_shared_subscriptions_count` | - |
| `mosquitto_broker_retained_messages` | `broker_retained_messages_count` | - |
| `mosquitto_broker_packet_out_count` | `broker_packet_out_count` | - |
| `mosquitto_broker_packet_out_bytes` | `broker_packet_out_bytes` | - |
| `mosquitto_broker_heap_current_bytes` | `broker_heap_current` | gains a `_bytes` unit suffix |
| `mosquitto_broker_heap_maximum_bytes` | `broker_heap_maximum` | gains a `_bytes` unit suffix |
| `mosquitto_broker_uptime_seconds` | `broker_uptime` | **Counter -> Gauge**, gains a `_seconds` unit suffix |

### New, with no old equivalent

| Metric | What it is |
|---|---|
| `mosquitto_up` | 1 while connected to the broker and its `$SYS` tree is ticking, 0 otherwise. Scrape this to alert on broker/exporter connectivity instead of `absent()` on an arbitrary broker metric. |
| `mosquitto_broker_info{mosquitto_version="..."}` | Info-metric pattern; always 1. `$SYS/broker/version` was previously read but never exported at all. |
| `mosquitto_exporter_unknown_topics_total` | Counts `$SYS` topics received that are not in the exporter's static export table (deprecated aliases, the 24 dropped `load/**` series, anything else unrecognised). Not a broker health signal - a sustained increase means the broker is publishing something this exporter version doesn't know about yet. |

`mosquitto_exporter_invalid_payloads_total` (a payload that didn't parse as a
number) and `mosquitto_exporter_dropped_topics_total` (the topic cache was
full) are unchanged names, both pre-existing.

## 2. Shape changes (not simple renames)

### `load/**`: 27 series collapse to 3

Only `load/connections/**` is kept, now as one metric with a `window` label
instead of three separately-named series:

| New | Old |
|---|---|
| `mosquitto_broker_load_connections{window="1m"}` | `broker_load_connections_1min` |
| `mosquitto_broker_load_connections{window="5m"}` | `broker_load_connections_5min` |
| `mosquitto_broker_load_connections{window="15m"}` | `broker_load_connections_15min` |

**The other 24 series are gone with no replacement**: every
`broker_load_{messages_received,messages_sent,publish_dropped,publish_received,publish_sent,bytes_received,bytes_sent,sockets}_{1,5,15}min`.
These were exponentially-weighted moving averages of a raw counter that is
also exported separately (e.g. `load_bytes_received_1min` was an EWMA of
`broker_bytes_received`). Replace any dashboard panel or alert built on one
of these with `rate()` over the corresponding `_total` counter and a
matching time window - `rate(mosquitto_broker_bytes_received_total[5m])`
instead of `broker_load_bytes_received_5min`, for example. There is no
recording-rule shim for these: the underlying broker no longer publishes the
data to alias.

### Bridge status: metric name -> label

| New | Old |
|---|---|
| `mosquitto_broker_bridge_connected{mosquitto_bridge_name="<id>"}` | `broker_connection_<remote_clientid>_state` |

The old name embedded the bridge's remote client ID *in the metric name
itself* - a cardinality-in-the-name problem, and one no static recording
rule can alias (the old name is different for every bridge). Rewrite any
per-bridge dashboard panel or alert to select on the
`mosquitto_bridge_name` label instead of matching a metric name pattern.

## 3. Recording rules for a transition window

Drop-in Prometheus recording rules that alias the new names back onto the
old ones, so existing dashboards and alerts keep working unmodified while
you migrate them. Delete this rule group once every consumer has moved to
the new names - it is a bridge, not a permanent shim.

**Caveats:**

- A recording rule always produces a gauge in Prometheus's own bookkeeping,
  regardless of the source type. This does not change the *values* it
  records (a counter's raw value stream is preserved byte-for-byte, so
  `rate()` against the recorded series still works correctly) - it only
  means tooling that inspects `# TYPE` will see `gauge` for the aliased
  series. If that matters to you, migrate the query instead of relying on
  the alias.
- For the three metrics whose type actually changed *for the better*
  (`clients_total`, `clients_maximum`, `uptime` - all were wrongly Counters),
  the recording rule still forwards the raw value, but any `rate()` query you
  had against the old name was already producing garbage before this
  release; use the aliased value directly (or `deriv()`), not `rate()`.
- There is no rule for the 24 dropped `load/**` series (§2) or for
  `broker_connection_<id>_state` (§2) - both require rewriting the
  consuming query, not aliasing.

```yaml
groups:
  - name: mosquitto-exporter-v1-compat
    rules:
      # Counters
      - record: broker_messages_received
        expr: mosquitto_broker_messages_received_total
      - record: broker_messages_sent
        expr: mosquitto_broker_messages_sent_total
      - record: broker_bytes_received
        expr: mosquitto_broker_bytes_received_total
      - record: broker_bytes_sent
        expr: mosquitto_broker_bytes_sent_total
      - record: broker_publish_bytes_received
        expr: mosquitto_broker_publish_bytes_received_total
      - record: broker_publish_bytes_sent
        expr: mosquitto_broker_publish_bytes_sent_total
      - record: broker_publish_messages_received
        expr: mosquitto_broker_publish_messages_received_total
      - record: broker_publish_messages_sent
        expr: mosquitto_broker_publish_messages_sent_total
      - record: broker_publish_messages_dropped
        expr: mosquitto_broker_publish_messages_dropped_total
      - record: broker_connections_socket_count
        expr: mosquitto_broker_connections_socket_total
      - record: broker_clients_expired
        expr: mosquitto_broker_clients_expired_total

      # Gauges
      - record: broker_clients_total
        expr: mosquitto_broker_clients_total
      - record: broker_clients_connected
        expr: mosquitto_broker_clients_connected
      - record: broker_clients_disconnected
        expr: mosquitto_broker_clients_disconnected
      - record: broker_clients_maximum
        expr: mosquitto_broker_clients_maximum
      # Both old names pointed at the same value (§1 duplicate collapse).
      - record: broker_messages_stored
        expr: mosquitto_broker_messages_stored
      - record: broker_store_messages_count
        expr: mosquitto_broker_messages_stored
      - record: broker_store_messages_bytes
        expr: mosquitto_broker_store_messages_bytes
      - record: broker_subscriptions_count
        expr: mosquitto_broker_subscriptions
      - record: broker_shared_subscriptions_count
        expr: mosquitto_broker_shared_subscriptions
      - record: broker_retained_messages_count
        expr: mosquitto_broker_retained_messages
      - record: broker_packet_out_count
        expr: mosquitto_broker_packet_out_count
      - record: broker_packet_out_bytes
        expr: mosquitto_broker_packet_out_bytes
      - record: broker_heap_current
        expr: mosquitto_broker_heap_current_bytes
      - record: broker_heap_maximum
        expr: mosquitto_broker_heap_maximum_bytes
      - record: broker_uptime
        expr: mosquitto_broker_uptime_seconds

      # load/connections: 3 series, window label -> 3 flat old names.
      - record: broker_load_connections_1min
        expr: mosquitto_broker_load_connections{window="1m"}
      - record: broker_load_connections_5min
        expr: mosquitto_broker_load_connections{window="5m"}
      - record: broker_load_connections_15min
        expr: mosquitto_broker_load_connections{window="15m"}
```

Validate the rule file before loading it: `promtool check rules
mosquitto-exporter-v1-compat.yml`.

## 4. Removed flags

### `--reset-metrics` / `-r` / `RESET_METRICS`

Removed outright, not deprecated. It used to zero every cached counter on
disconnect. That fabricates a counter reset on every broker flap and
corrupts `rate()`/`increase()` across the flap - a monitoring bug, not a
feature. `mosquitto_up` going to 0, combined with every `mosquitto_broker_*`
series going absent together while disconnected, now conveys "the broker is
unreachable" honestly, without lying about the counters. If you relied on
`--reset-metrics` to make a broker restart visible: alert on `mosquitto_up`
or on a counter reset (Prometheus's `rate()`/`increase()` already handle a
genuine counter reset correctly; `resets()` also exists if you need to count
them).

### `--pass` (argv password), already removed pre-v1.0.0

`--pass` never accepted a password on the command line as of this exporter's
Phase 1 hardening (arguments are visible via `ps`, shell history, and
`docker inspect`). Use `--pass-file <path>` (a file containing the password)
or the `MQTT_PASS` environment variable. An `--endpoint` containing embedded
credentials (`tcp://user:pass@host`) is also rejected at startup - move the
credentials to `--user`/`--pass-file` or `MQTT_USER`/`MQTT_PASS`.

## 5. Changed default: `--insecure-skip-verify`

Now defaults to **false** (previously the exporter had no certificate
verification at all when talking TLS to the broker). If you connect to a
broker presenting a self-signed or private-CA certificate, startup now fails
the TLS handshake unless you either:

- pass `--ca-cert <path>` to trust that specific CA (preferred - keeps
  verification on), or
- pass `--insecure-skip-verify` to explicitly opt back into the old,
  unverified behaviour (not recommended - the broker's identity is no
  longer authenticated at all).

A TLS certificate/key load failure now also fails startup outright instead
of logging a warning and continuing with a broken TLS configuration.

## 6. New flag: `--sys-interval` / `MQTT_SYS_INTERVAL`

Default **10s**, must be greater than zero. It sizes one thing only: the
`$SYS/broker/uptime` staleness-heartbeat window used to detect a broker that
is still TCP-connected but whose `$SYS` tree has stopped ticking (broker
misconfigured with `sys_interval 0`, or otherwise stalled) - `mosquitto_up`
goes to 0 if `uptime` hasn't advanced for 3x this duration. It does **not**
expire any other cached series by age; a healthy, unchanging value (e.g.
`publish_messages_dropped` sitting at 0) is reported for as long as the
connection and heartbeat stay healthy, however many `sys_interval`s that is.

Set this close to the broker's own `sys_interval` (default on the broker
side is also 10s) for the most accurate stall detection; a mismatch only
degrades the *window size* used for detecting a stall, it does not delete or
corrupt any data.

## 7. Upgrade checklist

- [ ] Load the recording rules in §3 if you are not ready to update every
      dashboard/alert immediately.
- [ ] Rewrite any panel/alert built on a `load/**` series other than
      `load_connections_*` to use `rate()` over the matching `_total`
      counter (§2) - there is no alias for these.
- [ ] Rewrite any per-bridge panel/alert to match on the
      `mosquitto_bridge_name` label instead of a dynamic metric name (§2).
- [ ] Remove `--reset-metrics`/`-r`/`RESET_METRICS` from your deployment
      config; replace any logic depending on it with a `mosquitto_up` alert
      (§4).
- [ ] Confirm no deployment still relies on embedded URL credentials or an
      unverified TLS broker connection; add `--ca-cert` or
      `--insecure-skip-verify` explicitly if needed (§4, §5).
- [ ] Set `--sys-interval`/`MQTT_SYS_INTERVAL` if your broker's own
      `sys_interval` is not the default 10s (§6).
