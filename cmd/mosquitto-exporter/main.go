package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/pauldeng/mosquitto-exporter/internal/exporter"
	"github.com/urfave/cli/v3"
)

const appName = "mosquitto-exporter"

var logLevel = new(slog.LevelVar)

func init() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})))
}

func parseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(value) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid --log-level %q: expected debug, info, warn, or error", value)
	}
}

func resolvePassword(passFile string) (string, error) {
	if passFile == "" {
		return os.Getenv("MQTT_PASS"), nil
	}
	//nolint:gosec // G304: passFile is the operator-supplied --pass-file
	data, err := os.ReadFile(passFile)
	if err != nil {
		return "", fmt.Errorf("reading --pass-file %s: %w", passFile, err)
	}
	return strings.TrimRight(string(data), "\r\n"), nil
}

func main() {
	if err := newCommand().Run(context.Background(), os.Args); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

func newCommand() *cli.Command {
	return &cli.Command{
		Name:    appName,
		Version: versionString(),
		Authors: []any{
			"Johan Ryberg <johan@securit.se>",
			"Arturo Reuschenbach Puncernau <a.reuschenbach.puncernau@sap.com>",
			"Fabian Ruff <fabian.ruff@sap.com>",
			"Paul Deng <dengpeng.cn@gmail.com>",
		},
		Usage: "Prometheus exporter for broker metrics",
		Before: func(ctx context.Context, cmd *cli.Command) (context.Context, error) {
			level, err := parseLogLevel(cmd.String("log-level"))
			if err != nil {
				return ctx, err
			}
			logLevel.Set(level)
			return ctx, nil
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			password, err := resolvePassword(cmd.String("pass-file"))
			if err != nil {
				return err
			}
			return exporter.Run(ctx, config.Config{
				Endpoint:           cmd.String("endpoint"),
				BindAddress:        cmd.String("bind-address"),
				User:               cmd.String("user"),
				Password:           password,
				CertFile:           cmd.String("cert"),
				KeyFile:            cmd.String("key"),
				CACertFile:         cmd.String("ca-cert"),
				ClientID:           cmd.String("client-id"),
				Version:            versionString(),
				InsecureSkipVerify: cmd.Bool("insecure-skip-verify"),
				SysInterval:        cmd.Duration("sys-interval"),
			})
		},
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "log-level", Usage: "Log level (debug|info|warn|error)", Value: "info", Sources: cli.EnvVars("LOG_LEVEL")},
			&cli.StringFlag{Name: "endpoint", Aliases: []string{"e"}, Usage: "Endpoint for the Mosquitto message broker", Value: "tcp://127.0.0.1:1883", Sources: cli.EnvVars("BROKER_ENDPOINT")},
			&cli.StringFlag{Name: "bind-address", Aliases: []string{"b"}, Usage: "Listen address for metrics HTTP endpoint", Value: "0.0.0.0:9234", Sources: cli.EnvVars("BIND_ADDRESS")},
			&cli.StringFlag{Name: "user", Aliases: []string{"u"}, Usage: "Username for the Mosquitto message broker", Sources: cli.EnvVars("MQTT_USER")},
			&cli.StringFlag{
				Name:    "pass-file",
				Usage:   "Path to a file containing the password for the Mosquitto broker user (password can also come from MQTT_PASS; never pass it on argv)",
				Sources: cli.EnvVars("MQTT_PASS_FILE"),
			},
			&cli.StringFlag{Name: "cert", Aliases: []string{"c"}, Usage: "Location of a TLS certificate .pem file for the Mosquitto message broker", Sources: cli.EnvVars("MQTT_CERT")},
			&cli.StringFlag{Name: "key", Aliases: []string{"k"}, Usage: "Location of a TLS private key .pem file for the Mosquitto message broker", Sources: cli.EnvVars("MQTT_KEY")},
			&cli.StringFlag{Name: "ca-cert", Usage: "Location of a PEM-encoded CA certificate used to verify the broker's TLS certificate", Sources: cli.EnvVars("MQTT_CA_CERT")},
			&cli.BoolFlag{Name: "insecure-skip-verify", Usage: "Disable TLS certificate verification of the broker (INSECURE - only for testing)", Sources: cli.EnvVars("MQTT_INSECURE_SKIP_VERIFY")},
			&cli.StringFlag{Name: "client-id", Aliases: []string{"i"}, Usage: "Client id to be used to connect to the Mosquitto message broker", Sources: cli.EnvVars("MQTT_CLIENT_ID")},
			&cli.DurationFlag{
				Name:    "sys-interval",
				Usage:   "Sizes the $SYS/broker/uptime staleness-heartbeat window only (must be > 0): while connected, the broker is reported down if uptime has not advanced for 3x this duration. It does not expire individual cached series and should match the broker's own sys_interval",
				Value:   10 * time.Second,
				Sources: cli.EnvVars("MQTT_SYS_INTERVAL"),
			},
		},
	}
}
