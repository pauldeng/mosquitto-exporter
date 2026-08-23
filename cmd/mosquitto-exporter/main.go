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
	cfgPath := config.ConfigFilePath(os.Args[1:], os.LookupEnv)
	fileCfg, err := config.LoadFile(cfgPath)
	if err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
	if err := newCommand(fileCfg).Run(context.Background(), os.Args); err != nil {
		slog.Error("exiting", "error", err)
		os.Exit(1)
	}
}

// or returns v if non-nil, otherwise def. It layers an optional YAML value
// in as a flag's default (DESIGN.md §7 Phase 5): explicit flag > env > YAML
// > this hardcoded default, because urfave/cli only falls back to a flag's
// Value when neither the command line nor its env Sources set it.
func or[T any](v *T, def T) T {
	if v != nil {
		return *v
	}
	return def
}

// logEffectiveConfig logs the resolved configuration once at startup
// (decision 10.12). It never logs cfg itself - a %v/%+v of Config would
// include Password - and it logs only whether a password was set, never
// its value, regardless of whether that value came from a flag, env,
// YAML, or --pass-file.
func logEffectiveConfig(logger *slog.Logger, cmd *cli.Command, cfg config.Config) {
	logger.Debug("effective configuration",
		"config_file", cmd.String("config"),
		"endpoint", cfg.Endpoint,
		"bind_address", cfg.BindAddress,
		"user", cfg.User,
		"password_set", cfg.Password != "",
		"pass_file", cmd.String("pass-file"),
		"cert", cfg.CertFile,
		"key", cfg.KeyFile,
		"ca_cert", cfg.CACertFile,
		"insecure_skip_verify", cfg.InsecureSkipVerify,
		"client_id", cfg.ClientID,
		"log_level", cmd.String("log-level"),
		"sys_interval", cfg.SysInterval.String(),
		"otlp_endpoint", cfg.OTLPEndpoint,
		"otlp_protocol", cfg.OTLPProtocol,
		"otlp_interval", cfg.OTLPInterval.String(),
	)
}

func newCommand(fc *config.FileConfig) *cli.Command {
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
			cfg := config.Config{
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
				OTLPEndpoint:       cmd.String("otlp-endpoint"),
				OTLPProtocol:       cmd.String("otlp-protocol"),
				OTLPInterval:       cmd.Duration("otlp-interval"),
			}
			logEffectiveConfig(slog.Default(), cmd, cfg)
			return exporter.Run(ctx, cfg)
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "config",
				Usage:   "Path to an optional YAML config file (precedence: flags > env > YAML > defaults). Not naming one, here or via CONFIG_FILE, is not an error; naming one that is missing or malformed is",
				Sources: cli.EnvVars("CONFIG_FILE"),
			},
			&cli.StringFlag{Name: "log-level", Usage: "Log level (debug|info|warn|error)", Value: or(fc.LogLevel, "info"), Sources: cli.EnvVars("LOG_LEVEL")},
			&cli.StringFlag{Name: "endpoint", Aliases: []string{"e"}, Usage: "Endpoint for the Mosquitto message broker", Value: or(fc.Endpoint, "tcp://127.0.0.1:1883"), Sources: cli.EnvVars("BROKER_ENDPOINT")},
			&cli.StringFlag{Name: "bind-address", Aliases: []string{"b"}, Usage: "Listen address for metrics HTTP endpoint", Value: or(fc.BindAddress, "0.0.0.0:9234"), Sources: cli.EnvVars("BIND_ADDRESS")},
			&cli.StringFlag{Name: "user", Aliases: []string{"u"}, Usage: "Username for the Mosquitto message broker", Value: or(fc.User, ""), Sources: cli.EnvVars("MQTT_USER")},
			&cli.StringFlag{
				Name:    "pass-file",
				Usage:   "Path to a file containing the password for the Mosquitto broker user (password can also come from MQTT_PASS; never pass it on argv, and never put it in the YAML config file)",
				Value:   or(fc.PassFile, ""),
				Sources: cli.EnvVars("MQTT_PASS_FILE"),
			},
			&cli.StringFlag{Name: "cert", Aliases: []string{"c"}, Usage: "Location of a TLS certificate .pem file for the Mosquitto message broker", Value: or(fc.Cert, ""), Sources: cli.EnvVars("MQTT_CERT")},
			&cli.StringFlag{Name: "key", Aliases: []string{"k"}, Usage: "Location of a TLS private key .pem file for the Mosquitto message broker", Value: or(fc.Key, ""), Sources: cli.EnvVars("MQTT_KEY")},
			&cli.StringFlag{Name: "ca-cert", Usage: "Location of a PEM-encoded CA certificate used to verify the broker's TLS certificate", Value: or(fc.CACert, ""), Sources: cli.EnvVars("MQTT_CA_CERT")},
			&cli.BoolFlag{Name: "insecure-skip-verify", Usage: "Disable TLS certificate verification of the broker (INSECURE - only for testing)", Value: or(fc.InsecureSkipVerify, false), Sources: cli.EnvVars("MQTT_INSECURE_SKIP_VERIFY")},
			&cli.StringFlag{Name: "client-id", Aliases: []string{"i"}, Usage: "Client id to be used to connect to the Mosquitto message broker", Value: or(fc.ClientID, ""), Sources: cli.EnvVars("MQTT_CLIENT_ID")},
			&cli.DurationFlag{
				Name:    "sys-interval",
				Usage:   "Sizes the $SYS/broker/uptime staleness-heartbeat window only (must be > 0): while connected, the broker is reported down if uptime has not advanced for 3x this duration. It does not expire individual cached series and should match the broker's own sys_interval",
				Value:   or(fc.SysInterval, 10*time.Second),
				Sources: cli.EnvVars("MQTT_SYS_INTERVAL"),
			},
			&cli.StringFlag{
				Name:    "otlp-endpoint",
				Usage:   "OTLP receiver endpoint to push metrics to (e.g. localhost:4317 for grpc, http://localhost:4318 for http); empty disables OTLP export entirely, no exporter is built and no OTLP network egress happens. Prometheus scraping via /metrics is always on and unaffected",
				Value:   or(fc.OTLPEndpoint, ""),
				Sources: cli.EnvVars("OTLP_ENDPOINT"),
			},
			&cli.StringFlag{
				Name:    "otlp-protocol",
				Usage:   "OTLP wire protocol to use when --otlp-endpoint is set: grpc or http",
				Value:   or(fc.OTLPProtocol, "grpc"),
				Sources: cli.EnvVars("OTLP_PROTOCOL"),
			},
			&cli.DurationFlag{
				Name:    "otlp-interval",
				Usage:   "Push interval for OTLP metric export when --otlp-endpoint is set (must be > 0)",
				Value:   or(fc.OTLPInterval, 60*time.Second),
				Sources: cli.EnvVars("OTLP_INTERVAL"),
			},
		},
	}
}
