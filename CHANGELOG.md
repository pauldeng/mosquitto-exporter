# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-08-23

A ground-up correctness, security, and metric-model pass. **Every metric this
exporter exports is renamed, and several defaults change.** Read
[`docs/MIGRATION.md`](docs/MIGRATION.md) in full before upgrading a production
instance - it has the complete old->new mapping table, drop-in Prometheus
recording rules for a transition window, and an upgrade checklist. This entry
only summarizes.

### BREAKING CHANGES

- **Every exported Prometheus metric is renamed** (`broker_messages_received`
  -> `mosquitto_broker_messages_received_total`, and so on for all of them).
  See [`docs/MIGRATION.md`](docs/MIGRATION.md) for the full mapping and
  compatibility recording rules.
- **Five metric types corrected**: `clients_total`, `clients_maximum`, and
  `uptime` were Counters and are now Gauges; `connections_socket_count` and
  `clients_expired` were Gauges and are now Counters. `rate()`/`irate()`
  queries against the old names were producing meaningless numbers.
- **`messages_stored` and `store_messages_count` collapse into one series**
  (`mosquitto_broker_messages_stored`) - both old names pointed at the same
  broker value, so it was double-published under two names.
- **24 of the 27 `load/**` EWMA series are dropped.** Only
  `load/connections/**` survives, and it collapses from three separately
  named series into one, `mosquitto_broker_load_connections`, carrying a
  `window` label (`1m`/`5m`/`15m`). The other 24 were exponentially-weighted
  averages of a raw counter that is also exported separately; use `rate()`
  over the corresponding `_total` counter instead. There is no alias for
  these - they are gone with no replacement metric.
- **Bridge status moves from the metric name into a label.** The old name
  embedded the bridge's remote client ID in the metric name itself
  (`broker_connection_<remote_clientid>_state`), which no static alias can
  cover since a new name exists per bridge. It's now
  `mosquitto_broker_bridge_connected{mosquitto_bridge_name="<id>"}`.
- **`--reset-metrics` / `-r` / `RESET_METRICS` is removed.** It used to zero
  every cached counter on a broker disconnect, which fabricated a counter
  reset on every flap and corrupted `rate()`/`increase()` across it.
  `mosquitto_up` going to 0, with every `mosquitto_broker_*` series going
  absent together while disconnected, now conveys the same fact honestly.
- **The broker password can no longer be passed on the command line.**
  `--pass` is gone; use `--pass-file <path>` or the `MQTT_PASS` environment
  variable. An `--endpoint` carrying embedded userinfo credentials
  (`tcp://user:pass@host`) is now rejected at startup - argv is visible via
  `ps`, shell history, and `docker inspect`, so the old behavior was never
  safe.
- **`--insecure-skip-verify` now defaults to `false`.** The exporter
  previously performed no TLS certificate verification at all against the
  broker. A broker presenting a self-signed or private-CA certificate that
  used to connect silently will now fail the TLS handshake at startup until
  you pass `--ca-cert <path>` (to trust that CA while keeping verification
  on) or explicitly opt back into the old behavior with
  `--insecure-skip-verify`.

### Added

- **OTLP metrics export**, opt-in via `--otlp-endpoint`/`--otlp-protocol`/
  `--otlp-interval`. Off by default: with no endpoint set, no OTLP exporter is
  constructed and no OTLP network egress happens. Prometheus scraping via
  `/metrics` is unaffected either way. See [`docs/OTEL.md`](docs/OTEL.md),
  including the documented OTLP counter-reset limitation across broker
  restarts.
- **YAML configuration file** via `--config`/`CONFIG_FILE`. Precedence is
  flags > environment variables > YAML > hardcoded defaults. Unknown keys and
  a `password:` key are both rejected at startup. See
  [`mosquitto-exporter.example.yaml`](mosquitto-exporter.example.yaml).
- **`mosquitto_up`** - 1 while connected to the broker and its `$SYS` tree is
  ticking, 0 otherwise. Alert on this instead of `absent()` on an arbitrary
  broker metric.
- **`mosquitto_broker_info{mosquitto_version="..."}`** - an info metric for
  the broker version, previously read from `$SYS/broker/version` but never
  exported at all.
- **`mosquitto_exporter_unknown_topics_total`** - counts `$SYS` publishes the
  exporter's topic table doesn't recognize, so a broker publishing something
  new is visible instead of silently ignored.
- **`--sys-interval`/`MQTT_SYS_INTERVAL`** (default `10s`, must be positive).
  Sizes the `$SYS/broker/uptime` staleness-heartbeat window: while connected,
  `mosquitto_up` goes to 0 if `uptime` hasn't advanced for 3x this duration.
- **Exporter self-metrics** (`process_*`, `go_*`) are now served from an
  explicit private registry rather than depending on incidental
  global-registry wiring, so they can't silently disappear or collide with
  another registration.
- **Loki log-shipping pipelines**, shipped as configuration rather than Go
  code: [`deploy/otelcol-contrib.yaml`](deploy/otelcol-contrib.yaml) and
  [`deploy/alloy.river`](deploy/alloy.river), with the choice between them,
  the file-vs-container-stdout distinction, and parsing caveats documented in
  [`docs/LOGS.md`](docs/LOGS.md). This exporter still does not tail
  `mosquitto.log` itself.
- **SBOM generated per release** via goreleaser's native `sboms:` support.

### Fixed

- **Two data races**, one of them a hard crash: a racy float64 counter
  update, and a concurrent map read/write that threw
  `fatal error: concurrent map read and map write` (not merely a `-race`
  warning - a process-killing fault under real traffic).
- **Two process-killing panics**: a panic on a negative counter payload, and
  a `MustRegister` panic on duplicate metric registration.
- **`/metrics` is now served before the broker connection is established.**
  Previously a down broker meant `/metrics` never came up at all; it now
  answers 200 with `mosquitto_up 0`.
- **TLS now fails closed.** A certificate or key load failure aborts startup
  instead of continuing with a broken/zero-value TLS configuration.
- **All four `http.Server` timeouts are now set** (previously zero/unbounded,
  leaving the server exposed to slow-client resource exhaustion).
- **Graceful shutdown**: SIGTERM now drains via `signal.NotifyContext`, the
  exit code is propagated correctly, and the OTel `MeterProvider` is flushed
  before exit.
- **Reconnect now uses exponential backoff with jitter** and rate-limited
  logging, replacing an unbounded/tight reconnect loop.

[1.0.0]: https://github.com/pauldeng/mosquitto-exporter/releases/tag/v1.0.0
