Mosquitto Exporter
=============================
Prometheus exporter for the [Mosquitto MQTT message broker](https://mosquitto.org/).

There is a docker image available:
```
docker run \
  -p 9234:9234 jryberg/mosquitto-exporter:v0.7.7 \
  --endpoint tcp://mosquitto:1883
```

Usage:
======

```
NAME:
   mosquitto-exporter - Prometheus exporter for broker metrics

USAGE:
   mosquitto-exporter [global options] command [command options] [arguments...]

VERSION:
   v0.7.7 (7b3248b0889448cc0f026604e537d84b1abdb09e), go1.24.6

AUTHORS:
   Johan Ryberg <johan@securit.se>
   Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>
   Fabian Ruff <fabian.ruff@sap.com>

COMMANDS:
   help, h  Shows a list of commands or help for one command

GLOBAL OPTIONS:
   --endpoint value, -e value      Endpoint for the Mosquitto message broker (default: "tcp://127.0.0.1:1883") [$BROKER_ENDPOINT]
   --bind-address value, -b value  Listen address for metrics HTTP endpoint (default: "0.0.0.0:9234") [$BIND_ADDRESS]
   --user value, -u value             Username for the Mosquitto message broker [$MQTT_USER]
   --pass-file value                  Path to a file containing the broker password [$MQTT_PASS_FILE]
   --cert value, -c value             Location of a TLS client certificate PEM file [$MQTT_CERT]
   --key value, -k value              Location of a TLS client private key PEM file [$MQTT_KEY]
   --ca-cert value                    Location of a PEM CA certificate used to verify the broker [$MQTT_CA_CERT]
   --insecure-skip-verify             Disable broker TLS certificate verification (default: false) [$MQTT_INSECURE_SKIP_VERIFY]
   --client-id value, -i value        Client id to be used to connect to the Mosquitto message broker [$MQTT_CLIENT_ID]
   --reset-metrics, -r                Reset metrics when losing connection to broker (default: true) [$RESET_METRICS]
   --help, -h                         show help
   --version, -v                      print the version
```

The broker password is never accepted as a command-line flag or as URL
userinfo because argv is visible through tools such as `ps` and
`docker inspect`. Set `MQTT_PASS` or use `--pass-file`; endpoints such as
`tcp://user:password@broker:1883` are rejected.
