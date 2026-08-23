Mosquitto Exporter
=============================
Prometheus exporter for the [Mosquitto MQTT message broker](https://mosquitto.org/).

A container image is built by `.goreleaser.yaml` on tagged releases, from
the `Dockerfile` in this repo (distroless base, non-root):
```
docker run \
  -p 9234:9234 pauldeng/mosquitto-exporter:<tag> \
  --endpoint tcp://mosquitto:1883
```

Usage:
======

```
NAME:
   mosquitto-exporter - Prometheus exporter for broker metrics

USAGE:
   mosquitto-exporter [global options]

VERSION:
   <release tag, git describe output, or dev for a plain go build>, go1.25.0

AUTHORS:
   Johan Ryberg <johan@securit.se>
   Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>
   Fabian Ruff <fabian.ruff@sap.com>
   Paul Deng <dengpeng.cn@gmail.com>

GLOBAL OPTIONS:
   --log-level string                Log level (debug|info|warn|error) (default: "info") [$LOG_LEVEL]
   --endpoint string, -e string      Endpoint for the Mosquitto message broker (default: "tcp://127.0.0.1:1883") [$BROKER_ENDPOINT]
   --bind-address string, -b string  Listen address for metrics HTTP endpoint (default: "0.0.0.0:9234") [$BIND_ADDRESS]
   --user string, -u string          Username for the Mosquitto message broker [$MQTT_USER]
   --pass-file string                Path to a file containing the password for the Mosquitto broker user (password can also come from MQTT_PASS; never pass it on argv) [$MQTT_PASS_FILE]
   --cert string, -c string          Location of a TLS certificate .pem file for the Mosquitto message broker [$MQTT_CERT]
   --key string, -k string           Location of a TLS private key .pem file for the Mosquitto message broker [$MQTT_KEY]
   --ca-cert string                  Location of a PEM-encoded CA certificate used to verify the broker's TLS certificate [$MQTT_CA_CERT]
   --insecure-skip-verify            Disable TLS certificate verification of the broker (INSECURE - only for testing) [$MQTT_INSECURE_SKIP_VERIFY]
   --client-id string, -i string     Client id to be used to connect to the Mosquitto message broker [$MQTT_CLIENT_ID]
   --sys-interval duration           Sizes the $SYS/broker/uptime staleness-heartbeat window only (must be > 0): while connected, the broker is reported down if uptime has not advanced for 3x this duration. It does not expire individual cached series and should match the broker's own sys_interval (default: 10s) [$MQTT_SYS_INTERVAL]
   --help, -h                        show help
   --version, -v                     print the version
```

`VERSION` is injected at build time from the nearest git tag (see
`VERSION` in the `Makefile` / `{{ .Tag }}` in `.goreleaser.yaml`). Plain
`go build` uses the neutral value `dev`; release versions are never
hand-maintained in source.

## Breaking changes since v0.7.7

v1.0.0 renames every exported metric and changes a few flags. **See
[`docs/MIGRATION.md`](docs/MIGRATION.md) for the full old->new metric mapping
table, drop-in Prometheus recording rules for a transition window, and the
complete upgrade checklist** - this section is only a summary:

- **Every Prometheus metric is renamed**, five wrong types are fixed
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
