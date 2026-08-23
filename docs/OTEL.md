# OpenTelemetry metrics export

mosquitto-exporter's metrics pipeline is a single OpenTelemetry
`MeterProvider` holding one instrument set (DESIGN.md §5.2). By default that
provider has exactly one reader attached: the one backing `/metrics`.
**Prometheus scraping is unchanged and remains the default** - existing
scrape configs need no changes, and a plain `mosquitto-exporter` process with
no OTLP flags opens no OTLP network connection at all.

OTLP push export is a second, optional reader on the same provider. It is
opt-in and off by default (DESIGN.md decision 10.6): leaving
`--otlp-endpoint`/`OTLP_ENDPOINT` unset means no OTLP exporter is ever
constructed and no OTLP egress happens, regardless of `--otlp-protocol` or
`--otlp-interval`.

## Turning on OTLP export

Set `--otlp-endpoint` (or `OTLP_ENDPOINT`) to your OTLP receiver's address:

```
OTEL_EXPORTER_OTLP_INSECURE=true mosquitto-exporter \
  --endpoint tcp://mosquitto:1883 \
  --otlp-endpoint localhost:4317 \
  --otlp-protocol grpc \
  --otlp-interval 30s
```

The `INSECURE` setting is only for a plaintext local collector. Omit it when
the receiver uses TLS.

| Flag | Env var | Default | Notes |
|---|---|---|---|
| `--otlp-endpoint` | `OTLP_ENDPOINT` | `""` (disabled) | OTLP receiver address, e.g. `localhost:4317` for grpc or `http://localhost:4318` for http |
| `--otlp-protocol` | `OTLP_PROTOCOL` | `grpc` | `grpc` or `http`; rejected at startup if anything else |
| `--otlp-interval` | `OTLP_INTERVAL` | `60s` | Push interval for the OTLP `PeriodicReader`; must be positive |

There is no `--otlp-headers`, `--otlp-insecure`, `--otlp-timeout`, or
resource-attribute flag. The OTel SDK itself already honours the standard
`OTEL_EXPORTER_OTLP_*` and `OTEL_RESOURCE_ATTRIBUTES` environment variables
for those concerns (TLS, headers, timeouts, resource attributes) - use those
directly rather than expecting this exporter to re-expose them. Credentials
in `--otlp-endpoint` are rejected; use `OTEL_EXPORTER_OTLP_HEADERS` for
authentication so secrets do not appear in argv.

## The Prometheus exporter is an experimental v0.x module

`/metrics` is served by `go.opentelemetry.io/otel/exporters/prometheus`,
which is v0.67.0 in OTel's `experimental-metrics` module set - it is *not*
covered by OTel's "metrics are stable" guarantee, and its own docs say the
default translation strategy may change in a future release (DESIGN.md
§5.2/§9.4). Practically, that means:

- The exact exported byte format is pinned deliberately (translation
  strategy, `target_info`/`otel_scope_*` disabled) rather than left to
  defaults, specifically so a routine dependency bump cannot silently rename
  every metric.
- A golden test over rendered `/metrics` output fails CI if any exported
  name ever changes, for any reason - that test is the tripwire for this
  risk, not a promise that the module can never break.
- The instruments themselves are built against the stable v1 `otel/metric`
  API; only the reader wiring touches the v0 module, confined to one file.

## OTLP counter resets are a known, accepted limitation

`$SYS` values (and therefore this exporter's counters) reset to zero when
the broker restarts. On the Prometheus leg this is harmless: classic
exposition carries no `StartTime`, a decrease just looks like an ordinary
counter reset, and Prometheus's own `rate()` reset detection handles it
exactly as it does today.

On the **OTLP leg this is a real gap**. The OTel SDK performs no
monotonicity or reset validation (verified in
`sdk/metric/internal/aggregate/sum.go`): a cumulative Sum is reported with
its `StartTime` never reset, so a broker restart produces a decreasing
cumulative value against a fixed `StartTime`. That is spec-invalid, and a
StartTime-aware OTLP backend may drop the point or mis-render it.

**This is deliberately not engineered around.** Hand-rolling delta tracking
to patch a secondary export path reintroduces exactly the complexity
observable instruments exist to avoid (DESIGN.md §5.3/§9.3). If you rely on
the OTLP leg and see this happen across a broker restart, that is expected
behavior, not a bug in this exporter; revisit only if a specific OTLP
backend demonstrably mis-renders it in a way that matters to you.

## No tracing

This exporter does not emit spans, and there is no Tempo or other tracing
backend integration (DESIGN.md §2, decision 10.5). It is a `$SYS`-polling
metrics exporter with a single incoming HTTP endpoint (`/metrics`) and no
request to trace across services - there is nothing here for a trace to
usefully connect. Do not expect spans from it.
