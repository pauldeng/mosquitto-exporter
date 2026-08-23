# Grafana dashboard and Prometheus alerts

Two files, both CI-linted (`.github/workflows/ci.yaml`, `dashboard-alerts` job):

- `deploy/dashboards/mosquitto.json` - a Grafana dashboard using the exact
  DESIGN.md §6 metric names. Import it directly (Dashboards -> New -> Import
  -> Upload JSON) and point the `datasource` variable at your Prometheus.
- `deploy/alerts/mosquitto.rules.yaml` - 9 Prometheus alerting rules, checked
  with `promtool check rules` in CI and locally via
  `docker run --rm --entrypoint promtool -v "$PWD/deploy/alerts/mosquitto.rules.yaml:/rules.yaml:ro" prom/prometheus:v3.9.1 check rules /rules.yaml`.
  Adjust the `job="mosquitto"` label in `MosquittoExporterScrapeFailed` to
  match your own scrape config - this exporter deliberately does not invent
  its own identity label (DESIGN.md §4.1).

## Dashboard layout

Five rows, each answering one operator question, in the order an operator
would ask them:

1. **Is the broker up?** - `mosquitto_up`, broker version/info, and an
   uptime trend panel (a downward step is a restart).
2. **Is it losing messages?** - dropped-publish rate, received/sent
   throughput, and the exporter's own three health counters
   (`mosquitto_exporter_{unknown_topics,dropped_topics,invalid_payloads}_total`),
   all of which read a constant 0 on a healthy, fully-recognised broker.
3. **Is it backing up?** - `mosquitto_broker_packet_out_{count,bytes}` and
   `mosquitto_broker_messages_stored`, explicitly labelled as saturation
   proxies. There is no latency metric and none can exist (DESIGN.md §4.2);
   this row does not pretend otherwise.
4. **Connections & clients** - the client-count gauges and subscription
   counts.
5. **Broker heap (not memory) & exporter self-health** - `heap_current`/
   `heap_maximum` labelled explicitly as broker heap, not process memory,
   with a text panel pointing at `node_exporter`/`cAdvisor`/`process-exporter`
   for real broker CPU/RSS (DESIGN.md §4.1); the exporter's own
   `process_resident_memory_bytes`/`process_cpu_seconds_total`; and bridge
   connectivity.

## Alert rules

| Alert | Trigger | Severity |
|---|---|---|
| `MosquittoBrokerDown` | `mosquitto_up == 0` for 2m | critical |
| `MosquittoExporterScrapeFailed` | scrape-level `up{job="mosquitto"} == 0` for 2m | critical |
| `MosquittoBrokerRestarted` | `delta(uptime_seconds[5m]) < 0` for 1m | warning |
| `MosquittoPublishMessagesDropping` | `rate(publish_messages_dropped_total[5m]) > 0` for 5m | warning |
| `MosquittoOutboundQueueGrowing` | `deriv(packet_out_count[10m]) > 0` for 10m | warning |
| `MosquittoUnknownTopicsDetected` | `increase(unknown_topics_total[10m]) > 0` for 5m | warning |
| `MosquittoTrackedTopicCacheFull` | `increase(dropped_topics_total[10m]) > 0` for 5m | warning |
| `MosquittoInvalidPayloadsDetected` | `increase(invalid_payloads_total[10m]) > 0` for 5m | warning |
| `MosquittoBridgeDisconnected` | `bridge_connected == 0` for 5m | warning |

Deliberately no per-series `absent()` alerts: per DESIGN.md §5.3, a
disconnected exporter or a stalled `$SYS` heartbeat takes every
`mosquitto_broker_*` series absent together, so an `absent()` alert per
series would fire ~28 times for one cause. `MosquittoBrokerDown` is the one
alert for that failure mode.

## Verified broker-version caveat: three §6 metrics need Mosquitto >= 2.1

Cross-checking the dashboard and alert rules against a live broker's real
`/metrics` output (this repo's own dev host, Mosquitto 2.0.22) turned up a
gap in the upstream research this project is built on, worth recording here
since it affects what an operator should expect to see:

`mosquitto_broker_connections_socket_total`, `mosquitto_broker_packet_out_count`,
and `mosquitto_broker_packet_out_bytes` never appeared - not even as an
initial retained snapshot - despite induced socket-connect traffic across
several `$SYS` publish ticks. Diffing `src/sys_tree.c` between the
`v2.0.22` and `v2.1.2` tags confirms why: Mosquitto 2.1 introduced a
generic `metrics[]` table as part of its internal rewrite, and that table is
where these three topics (`$SYS/broker/connections/socket/count`,
`$SYS/broker/packet/out/{count,bytes}`) were first declared. They do not
exist in 2.0.x's older global-counter code path at all - DESIGN.md's claim
that the two versions' `$SYS` topic sets are byte-for-byte identical does
not hold for these three.

Practical effect: on a Mosquitto 2.0.x broker these three series will never
appear on `/metrics`, by design of the broker rather than a bug in the
exporter or this dashboard - they are absent, not zero, exactly like any
other never-observed topic (DESIGN.md §5.3). On a 2.1+ broker they behave as
documented in §6. The dashboard and `MosquittoOutboundQueueGrowing` still
reference them under the instruction to use the exact §6 names; this note
exists so a 2.0.x operator seeing permanently-empty panels for these three
series knows it is expected, not a misconfiguration.
