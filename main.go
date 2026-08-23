package main

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"crypto/tls"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	log "github.com/sirupsen/logrus"
	"github.com/urfave/cli/v3"
)

const (
	appName = "mosquitto-exporter"
)

func main() {
	cmd := &cli.Command{
		Name:    appName,
		Version: versionString(),
		Authors: []any{
			"Johan Ryberg <johan@securit.se>",
			"Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>",
			"Fabian Ruff <fabian.ruff@sap.com>",
		},
		Usage:  "Prometheus exporter for broker metrics",
		Action: runServer,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "endpoint",
				Aliases: []string{"e"},
				Usage:   "Endpoint for the Mosquitto message broker",
				Sources: cli.EnvVars("BROKER_ENDPOINT"),
				Value:   "tcp://127.0.0.1:1883",
			},
			&cli.StringFlag{
				Name:    "bind-address",
				Aliases: []string{"b"},
				Usage:   "Listen address for metrics HTTP endpoint",
				Value:   "0.0.0.0:9234",
				Sources: cli.EnvVars("BIND_ADDRESS"),
			},
			&cli.StringFlag{
				Name:    "user",
				Aliases: []string{"u"},
				Usage:   "Username for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_USER"),
			},
			&cli.StringFlag{
				Name:    "pass",
				Aliases: []string{"p"},
				Usage:   "Password for the User on the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_PASS"),
			},
			&cli.StringFlag{
				Name:    "cert",
				Aliases: []string{"c"},
				Usage:   "Location of a TLS certificate .pem file for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_CERT"),
			},
			&cli.StringFlag{
				Name:    "key",
				Aliases: []string{"k"},
				Usage:   "Location of a TLS private key .pem file for the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_KEY"),
			},
			&cli.StringFlag{
				Name:    "client-id",
				Aliases: []string{"i"},
				Usage:   "Client id to be used to connect to the Mosquitto message broker",
				Value:   "",
				Sources: cli.EnvVars("MQTT_CLIENT_ID"),
			},
			&cli.BoolFlag{
				Name:    "reset-metrics",
				Aliases: []string{"r"},
				Usage:   "Reset metrics when loosing connection to broker",
				Value:   true,
				Sources: cli.EnvVars("RESET_METRICS"),
			},
		},
	}

	cmd.Run(context.Background(), os.Args)
}

func runServer(ctx context.Context, cmd *cli.Command) error {
	log.Infof("Starting %s %s", appName, versionString())

	opts := mqtt.NewClientOptions()
	opts.SetCleanSession(true)
	opts.AddBroker(cmd.String("endpoint"))

	if cmd.String("client-id") != "" {
		opts.SetClientID(cmd.String("client-id"))
	}

	// if you have a username you'll need a password with it
	if cmd.String("user") != "" {
		opts.SetUsername(cmd.String("user"))
		if cmd.String("pass") != "" {
			opts.SetPassword(cmd.String("pass"))
		}
	}
	// if you have a client certificate you want a key aswell
	if cmd.String("cert") != "" && cmd.String("key") != "" {
		keyPair, err := tls.LoadX509KeyPair(cmd.String("cert"), cmd.String("key"))
		if err != nil {
			log.Errorf("Failed to load certificate/keypair: %s", err)
		}
		tlsConfig := &tls.Config{
			Certificates:       []tls.Certificate{keyPair},
			InsecureSkipVerify: true,
			ClientAuth:         tls.NoClientCert,
		}
		opts.SetTLSConfig(tlsConfig)
		if !strings.HasPrefix(cmd.String("endpoint"), "ssl://") &&
			!strings.HasPrefix(cmd.String("endpoint"), "tls://") {
			log.Println("Warning: To use TLS the endpoint URL will have to begin with 'ssl://' or 'tls://'")
		}
	} else if (cmd.String("cert") != "" && cmd.String("key") == "") ||
		(cmd.String("cert") == "" && cmd.String("key") != "") {
		log.Println("Warning: For TLS to work both certificate and private key are needed. Skipping TLS.")
	}

	opts.OnConnect = func(client mqtt.Client) {
		log.Infof("Connected to %s", cmd.String("endpoint"))
		// subscribe on every (re)connect
		token := client.Subscribe("$SYS/#", 0, func(_ mqtt.Client, msg mqtt.Message) {
			processUpdate(msg.Topic(), string(msg.Payload()))
		})
		if !token.WaitTimeout(10 * time.Second) {
			log.Println("Error: Timeout subscribing to topic $SYS/#")
		}
		if err := token.Error(); err != nil {
			log.Errorf("Failed to subscribe to topic $SYS/#: %s", err)
		}
	}
	opts.OnConnectionLost = func(client mqtt.Client, err error) {
		if cmd.Bool("reset-metrics") {
			log.Warnf("Error: Connection to %s lost: %s, resetting counters", cmd.String("endpoint"), err)
			resetMetrics()
		} else {
			log.Warnf("Error: Connection to %s lost: %s", cmd.String("endpoint"), err)
		}
	}
	client := mqtt.NewClient(opts)

	// try to connect forever
	for {
		token := client.Connect()
		if token.WaitTimeout(5 * time.Second) {
			if token.Error() == nil {
				break
			}
			log.Errorf("Error: Failed to connect to broker: %s", token.Error())
		} else {
			log.Errorf("Timeout connecting to endpoint %s", cmd.String("endpoint"))
		}
		time.Sleep(5 * time.Second)
	}
	log.Infof("Connected to %s", cmd.String("endpoint"))

	// init the router and server
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", serveVersion)
	log.Infof("Listening on %s...", cmd.String("bind-address"))
	err := http.ListenAndServe(cmd.String("bind-address"), nil)
	fatalfOnError(err, "Failed to bind on %s: ", cmd.String("bind-address"))
	return nil
}

// $SYS/broker/bytes/received
func fatalfOnError(err error, msg string, args ...interface{}) {
	if err != nil {
		log.Fatalf(msg, args...)
	}
}
