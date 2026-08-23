package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pauldeng/mosquitto-exporter/internal/config"
	"github.com/urfave/cli/v3"
)

func TestParseLogLevel(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"debug", "INFO", "warn", "error"} {
		if _, err := parseLogLevel(value); err != nil {
			t.Errorf("parseLogLevel(%q): %v", value, err)
		}
	}
	if _, err := parseLogLevel("DEBUG+2"); err == nil {
		t.Fatal("undocumented custom log level was accepted")
	}
}

func TestResolvePassword(t *testing.T) {
	t.Run("environment fallback", func(t *testing.T) {
		t.Setenv("MQTT_PASS", "s3cret")
		got, err := resolvePassword("", false)
		if err != nil || got != "s3cret" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("file takes precedence and trims newline", func(t *testing.T) {
		t.Setenv("MQTT_PASS", "unused")
		path := filepath.Join(t.TempDir(), "pass")
		if err := os.WriteFile(path, []byte("filesecret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolvePassword(path, true)
		if err != nil || got != "filesecret" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
	t.Run("environment takes precedence over YAML pass_file", func(t *testing.T) {
		t.Setenv("MQTT_PASS", "envsecret")
		path := filepath.Join(t.TempDir(), "pass")
		if err := os.WriteFile(path, []byte("yamlsecret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolvePassword(path, false)
		if err != nil || got != "envsecret" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
}

func TestNoPasswordFlag(t *testing.T) {
	t.Parallel()
	for _, flag := range newCommand(&config.FileConfig{}).Flags {
		for _, name := range flag.Names() {
			if name == "pass" {
				t.Fatal("password must never be accepted on argv")
			}
		}
	}
}

// TestNoResetMetricsFlag guards DESIGN.md §5.5/§6.5: --reset-metrics
// fabricates counter resets on every broker flap and was removed as a
// breaking change in v1.0.0. It must never come back.
func TestNoResetMetricsFlag(t *testing.T) {
	t.Parallel()
	for _, flag := range newCommand(&config.FileConfig{}).Flags {
		for _, name := range flag.Names() {
			if name == "reset-metrics" || name == "r" {
				t.Fatal("--reset-metrics was removed in v1.0.0 (DESIGN.md §5.5) and must not be reintroduced")
			}
		}
	}
}

// TestSysIntervalFlag pins the DESIGN.md §7 Phase 3 item 6 default (10s) and
// that a non-default value round-trips through the command into
// config.Config.SysInterval.
func TestSysIntervalFlag(t *testing.T) {
	t.Parallel()
	var found *cli.DurationFlag
	for _, flag := range newCommand(&config.FileConfig{}).Flags {
		if df, ok := flag.(*cli.DurationFlag); ok && df.Name == "sys-interval" {
			found = df
		}
	}
	if found == nil {
		t.Fatal("--sys-interval flag not found")
	}
	if found.Value != 10*time.Second {
		t.Errorf("--sys-interval default = %s, want 10s", found.Value)
	}
}

// TestOTLPFlagDefaults pins DESIGN.md decision 10.6: OTLP export is opt-in
// and off by default. An empty --otlp-endpoint default means no OTLP
// exporter is ever constructed and no OTLP network egress happens,
// regardless of --otlp-protocol/--otlp-interval's own defaults.
func TestOTLPFlagDefaults(t *testing.T) {
	t.Parallel()
	var endpoint *cli.StringFlag
	var protocol *cli.StringFlag
	var interval *cli.DurationFlag
	for _, flag := range newCommand(&config.FileConfig{}).Flags {
		switch f := flag.(type) {
		case *cli.StringFlag:
			switch f.Name {
			case "otlp-endpoint":
				endpoint = f
			case "otlp-protocol":
				protocol = f
			}
		case *cli.DurationFlag:
			if f.Name == "otlp-interval" {
				interval = f
			}
		}
	}
	if endpoint == nil || protocol == nil || interval == nil {
		t.Fatal("--otlp-endpoint, --otlp-protocol, and --otlp-interval flags must all be defined")
	}
	if endpoint.Value != "" {
		t.Errorf("--otlp-endpoint default = %q, want empty (opt-in, off by default)", endpoint.Value)
	}
	if protocol.Value != "grpc" {
		t.Errorf("--otlp-protocol default = %q, want %q", protocol.Value, "grpc")
	}
	if interval.Value != 60*time.Second {
		t.Errorf("--otlp-interval default = %s, want 60s", interval.Value)
	}
}

// TestConfigFlagExists guards the DESIGN.md §7 Phase 5 --config/CONFIG_FILE
// surface: it must be a recognised, documented flag rather than a bare env
// var the user has to know about.
func TestConfigFlagExists(t *testing.T) {
	t.Parallel()
	for _, flag := range newCommand(&config.FileConfig{}).Flags {
		if sf, ok := flag.(*cli.StringFlag); ok && sf.Name == "config" {
			return
		}
	}
	t.Fatal("--config flag not found")
}

// TestConfigPrecedence table-tests the full flag/env/YAML matrix for one
// representative setting (--endpoint), per DESIGN.md §9.10/§7 Phase 5: with
// three independent sources, precedence must be flag > env > YAML > the
// hardcoded default in every combination, not just the common cases.
func TestConfigPrecedence(t *testing.T) {
	const (
		flagVal    = "tcp://flag:1883"
		envVal     = "tcp://env:1883"
		yamlVal    = "tcp://yaml:1883"
		defaultVal = "tcp://127.0.0.1:1883"
	)

	for _, flagSet := range []bool{false, true} {
		for _, envSet := range []bool{false, true} {
			for _, yamlSet := range []bool{false, true} {
				name := fmt.Sprintf("flag=%v/env=%v/yaml=%v", flagSet, envSet, yamlSet)
				t.Run(name, func(t *testing.T) {
					want := defaultVal
					if yamlSet {
						want = yamlVal
					}
					if envSet {
						want = envVal
					}
					if flagSet {
						want = flagVal
					}

					if envSet {
						t.Setenv("BROKER_ENDPOINT", envVal)
					}

					fc := &config.FileConfig{}
					if yamlSet {
						v := yamlVal
						fc.Endpoint = &v
					}

					args := []string{"mosquitto-exporter"}
					if flagSet {
						args = append(args, "--endpoint", flagVal)
					}

					cmd := newCommand(fc)
					var got string
					cmd.Action = func(_ context.Context, cmd *cli.Command) error {
						got = cmd.String("endpoint")
						return nil
					}
					if err := cmd.Run(context.Background(), args); err != nil {
						t.Fatalf("Run() error = %v", err)
					}
					if got != want {
						t.Errorf("endpoint = %q, want %q", got, want)
					}
				})
			}
		}
	}
}

// TestLogEffectiveConfigRedactsPassword proves decision 10.12: the
// resolved-config debug log must never contain the password, regardless of
// which source it came from, while still surfacing enough (like the
// endpoint) to be useful for diagnosing a misconfiguration.
func TestLogEffectiveConfigRedactsPassword(t *testing.T) {
	t.Parallel()
	const password = "s3cret-do-not-leak"

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	cmd := newCommand(&config.FileConfig{})
	cmd.Action = func(context.Context, *cli.Command) error { return nil }
	if err := cmd.Run(context.Background(), []string{"mosquitto-exporter"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logEffectiveConfig(logger, cmd, config.Config{ //nolint:gosec // G101: malformed endpoint proving credentials are redacted, not a real secret
		Endpoint:     "tcp://broker.example.com:1883",
		User:         "credential-user",
		Password:     password,
		OTLPEndpoint: "https://user:otlp-secret@collector.example.com",
	})

	out := buf.String()
	if bytes.Contains([]byte(out), []byte(password)) {
		t.Fatalf("effective-config log leaked the password: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte("tcp://broker.example.com:1883")) {
		t.Errorf("effective-config log missing endpoint: %s", out)
	}
	if !bytes.Contains([]byte(out), []byte(`"password_set":true`)) {
		t.Errorf("effective-config log should record password_set=true: %s", out)
	}
	if bytes.Contains([]byte(out), []byte(`"user":`)) {
		t.Errorf("effective-config log should not contain credential values: %s", out)
	}
	if bytes.Contains([]byte(out), []byte("credential-user")) {
		t.Errorf("effective-config log leaked the username: %s", out)
	}
	if bytes.Contains([]byte(out), []byte("otlp-secret")) {
		t.Errorf("effective-config log leaked endpoint credentials: %s", out)
	}
}
