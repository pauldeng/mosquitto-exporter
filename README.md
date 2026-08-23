Mosquitto Exporter
=============================
Prometheus exporter for the [Mosquitto MQTT message broker](https://mosquitto.org/).
It holds an MQTT subscription to the broker's `$SYS` tree and re-exports
those values as Prometheus/OTLP metrics on `/metrics`.

## Quick start

A container image is built by `.goreleaser.yaml` on tagged releases, from
the `Dockerfile` in this repo (distroless base, non-root):
```
docker run \
  -p 9234:9234 pauldeng/mosquitto-exporter:<tag> \
  --bind-address 0.0.0.0:9234 \
  --endpoint tcp://mosquitto:1883
```
Then scrape `http://<host>:9234/metrics` from Prometheus. `mosquitto_up`
reports connectivity to the broker; every other series is prefixed
`mosquitto_broker_*`, `mosquitto_exporter_*`, `process_*`, or `go_*`.
The default listener is `127.0.0.1:9234`; the container example opts into
all interfaces because Docker port publishing requires it. Expose that port
only to a trusted monitoring network or an authenticating reverse proxy.

## Usage

```
NAME:
   mosquitto-exporter - Prometheus exporter for broker metrics

USAGE:
   mosquitto-exporter [global options]

VERSION:
   <release tag or dev for a plain go build>, <Go runtime version>

AUTHORS:
   Johan Ryberg <johan@securit.se>
   Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>
   Fabian Ruff <fabian.ruff@sap.com>
   Paul Deng <dengpeng.cn@gmail.com>

GLOBAL OPTIONS:
   --config string                   Path to an optional YAML config file (precedence: flags > env > YAML > defaults). Not naming one, here or via CONFIG_FILE, is not an error; naming one that is missing or malformed is a startup error [$CONFIG_FILE]
   --log-level string                Log level (debug|info|warn|error) (default: "info") [$LOG_LEVEL]
   --endpoint string, -e string      Endpoint for the Mosquitto message broker; credentials require TLS or a local unix socket (default: "tcp://127.0.0.1:1883") [$BROKER_ENDPOINT]
   --bind-address string, -b string  Listen address for metrics HTTP endpoint (default: "127.0.0.1:9234") [$BIND_ADDRESS]
   --user string, -u string          Username for the Mosquitto message broker (requires TLS or a local unix socket) [$MQTT_USER]
   --pass-file string                Path to a file containing the password for the Mosquitto broker user (password can also come from MQTT_PASS; never pass it on argv, and never put it in the YAML config file) [$MQTT_PASS_FILE]
   --cert string, -c string          Location of a TLS certificate .pem file for the Mosquitto message broker [$MQTT_CERT]
   --key string, -k string           Location of a TLS private key .pem file for the Mosquitto message broker [$MQTT_KEY]
   --ca-cert string                  Location of a PEM-encoded CA certificate used to verify the broker's TLS certificate [$MQTT_CA_CERT]
   --insecure-skip-verify            Disable TLS certificate verification of the broker (INSECURE - only for testing) [$MQTT_INSECURE_SKIP_VERIFY]
   --client-id string, -i string     Client id to be used to connect to the Mosquitto message broker [$MQTT_CLIENT_ID]
   --sys-interval duration           Sizes the $SYS/broker/uptime staleness-heartbeat window only (must be > 0): while connected, the broker is reported down if uptime has not advanced for 3x this duration. It does not expire individual cached series and should match the broker's own sys_interval (default: 10s) [$MQTT_SYS_INTERVAL]
   --otlp-endpoint string            EXPERIMENTAL: OTLP receiver endpoint to push metrics to; empty disables OTLP export. Counter resets after broker restarts are not yet represented correctly in OTLP [$OTLP_ENDPOINT]
   --otlp-protocol string            OTLP wire protocol to use when --otlp-endpoint is set: grpc or http (default: "grpc") [$OTLP_PROTOCOL]
   --otlp-interval duration          Push interval for OTLP metric export when --otlp-endpoint is set (must be > 0) (default: 1m0s) [$OTLP_INTERVAL]
   --help, -h                        show help
   --version, -v                     print the version
```

`VERSION` is injected from the release tag by `.goreleaser.yaml`. Plain
`go build` uses the neutral value `dev`; release versions are never
hand-maintained in source. `make build` is only a wrapper around GoReleaser,
so local snapshots and tagged releases use the same build configuration.

OTLP metrics export (`--otlp-endpoint`/`--otlp-protocol`/`--otlp-interval`)
is **experimental**, opt-in, and off by default. Prometheus scraping via
`/metrics` is production-supported and unaffected. See
[`docs/OTEL.md`](docs/OTEL.md) for the OTLP counter-reset limitation.

## Configuration

Every setting above can also come from a YAML config file. **Precedence is
flags > environment variables > YAML > the hardcoded defaults shown in
`--help`.** A setting only ever falls through to a lower-precedence source
when the higher one didn't provide it - an explicit flag always wins, even
if it happens to match the default.

Point at a config file with `--config <path>` or the `CONFIG_FILE`
environment variable (the flag wins if both are set). **The file is
optional**: not naming one - which is the default - is not an error, and
the exporter behaves exactly as it would with no config file support at
all. Naming one that doesn't exist, or that fails to parse, *is* a startup
error, since at that point it was named on purpose.

The YAML keys mirror the flags one-for-one (`bind_address` for
`--bind-address`, `sys_interval` for `--sys-interval`, and so on) - see
[`mosquitto-exporter.example.yaml`](mosquitto-exporter.example.yaml) for
the full, commented list with every default shown. Two things the loader
enforces on purpose:

- **Unknown keys are a startup error.** A typo'd key (`endpiont:`) is
  otherwise indistinguishable from "not set" and would silently fall back
  to the default.
- **There is no `password` key.** A YAML file is something people are
  encouraged to commit; a password field would make that unsafe. Supply a
  password only via `--pass-file <path>` (a file containing it, settable
  from YAML as `pass_file`) or the `MQTT_PASS` environment variable. A
  `password:` key in the file is rejected outright, with an error
  explaining why.

For the password specifically, an explicit `--pass-file` or
`MQTT_PASS_FILE` wins over `MQTT_PASS`; otherwise `MQTT_PASS` wins over a
YAML `pass_file`, preserving the environment-over-YAML rule.

Broker credentials are accepted only over TLS (`ssl://`, `tls://`,
`mqtts://`, `mqtt+ssl://`, `tcps://`, or `wss://`) or a local `unix://`
socket. Plaintext transports may be used only without credentials. TLS
options on a plaintext endpoint are rejected instead of being ignored.

At `--log-level debug`, the exporter logs the fully-resolved configuration
once at startup - which value won, from whichever source - as a single
`"effective configuration"` line. Credential values are omitted: username
and password are represented only as `user_set`/`password_set` booleans.

## Scope: what this exporter does not do

- **No broker CPU or process memory.** `$SYS` publishes no CPU metric at
  all, and its heap counters are not process memory:
  `mosquitto_broker_heap_current_bytes`/`_maximum_bytes` are mosquitto's own
  *tracked heap*, excluding stacks, allocator fragmentation, shared
  libraries, and TLS buffers - they read lower than `ps`/`top` would. For
  real broker CPU/RSS, run a separate exporter scraped into the same
  Prometheus: `cAdvisor` for a containerized broker, `process-exporter` for
  a systemd/bare-process one, joined to this exporter's series via a shared
  `instance`/`broker` label in your scrape config, not by anything this
  exporter adds.
- **No message-latency metric, and none can be derived.** `$SYS` contains no
  timing data at all - no histogram, percentile, or queue-wait time. The
  closest proxies are `mosquitto_broker_packet_out_count`/`_bytes`
  (delivery backlog) and `mosquitto_broker_publish_messages_dropped_total`
  (backlog overflow); do not label a dashboard built on either "latency".
  Measuring real latency requires a synthetic probe client, publisher/
  subscriber-side instrumentation, or a broker plugin - all outside this
  exporter.
- **No tracing.** This exporter emits no spans and integrates with no
  tracing backend (Tempo included). It is a `$SYS`-polling metrics exporter
  with one incoming HTTP endpoint (`/metrics`); there is no request path
  here worth tracing.
- **No log tailing.** `mosquitto.log` shipping to Loki is configuration for
  a collector you already run (`otelcol-contrib` or Grafana Alloy), not a
  feature of this binary. See [`docs/LOGS.md`](docs/LOGS.md).

## Breaking changes since v0.7.7

v1.0.0 renames every broker-derived metric and changes a few flags. **See
[`docs/MIGRATION.md`](docs/MIGRATION.md) for the full old->new metric mapping
table, drop-in Prometheus recording rules for a transition window, and the
complete upgrade checklist**, or [`CHANGELOG.md`](CHANGELOG.md) for the full
release history. This section is only a summary:

- **Every broker-derived Prometheus metric is renamed**, while `process_*`
  and `go_*` keep their existing names. Five wrong types are fixed
  (`clients_total`, `clients_maximum`, `uptime` were Counters and are now
  Gauges; `connections_socket_count`, `clients_expired` were Gauges and are
  now Counters), two duplicate series collapse into one, and the 24
  redundant `load/**` EWMA series are dropped (their raw counters remain,
  renamed, so `rate()` replaces them). Bridge status moves from the metric
  name into a `mosquitto_bridge_name` label. Full mapping:
  [`docs/MIGRATION.md`](docs/MIGRATION.md).
- **`--reset-metrics`/`-r`/`RESET_METRICS` is removed.** It used to zero
  every cached counter on disconnect, which fabricates a counter reset on
  every broker flap. `mosquitto_up` plus every `mosquitto_broker_*` series
  going absent together while disconnected now conveys the same fact
  honestly.
- **The broker password can no longer be passed on the command line.**
  `--pass` is gone. Use `--pass-file <path>` (a file containing the
  password) or the `MQTT_PASS` environment variable. An endpoint carrying
  embedded credentials (`tcp://user:pass@host`) is also rejected. Argv is
  visible through `ps`, shell history, and `docker inspect`, so the old
  behaviour was never safe.
- **TLS certificate verification is on by default.** `--insecure-skip-verify`
  (default `false`) opts back into the old, unverified behaviour; `--ca-cert`
  lets you trust a private/self-signed broker CA without disabling
  verification at all. A cert-load failure now fails startup instead of
  silently continuing with a broken TLS config.
- **New `--sys-interval`/`MQTT_SYS_INTERVAL` flag** (default `10s`, must be
  positive). It sizes the `$SYS/broker/uptime` staleness-heartbeat window
  only: while connected, `mosquitto_up` goes to 0 if `uptime` has not
  advanced for 3x this duration (the broker's `$SYS` tree has stalled). It
  does not expire any other cached series by age - a healthy, unchanging
  value is reported for as long as the connection and heartbeat stay
  healthy.

## Further reading

- [`docs/MIGRATION.md`](docs/MIGRATION.md) - upgrading from v0.7.7: full
  metric mapping, recording rules, upgrade checklist.
- [`docs/OTEL.md`](docs/OTEL.md) - OTLP export details, the experimental
  Prometheus exporter dependency, and the OTLP counter-reset limitation.
- [`docs/LOGS.md`](docs/LOGS.md) - shipping `mosquitto.log` to Loki via
  `otelcol-contrib` or Grafana Alloy (this exporter does not tail files).
- [`mosquitto-exporter.example.yaml`](mosquitto-exporter.example.yaml) - every
  YAML key, commented, with its default.
- [`CHANGELOG.md`](CHANGELOG.md) - full release history.
- [`SECURITY.md`](SECURITY.md) - supported versions and private vulnerability reporting.

## Development notes

- `make lint` can panic on a workstation with a newer Go than your installed
  golangci-lint supports (`file requires newer Go version goX.Y`) instead of
  just warning - this was reproduced with golangci-lint v2.12.2 (built with
  go1.26) against a go1.27 toolchain on 2026-08-23. It has since been
  observed working with golangci-lint v2.13.1 (built with go1.27.0), so
  treat this as a version-pairing gotcha rather than a fixed rule: if it
  hits you, either update golangci-lint or point `PATH` at the Go version
  named in `go.mod`. It never affects CI, which pins the lint job to that
  same version via `go-version-file`.
