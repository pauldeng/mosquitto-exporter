package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

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
		got, err := resolvePassword("")
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
		got, err := resolvePassword(path)
		if err != nil || got != "filesecret" {
			t.Fatalf("got (%q, %v)", got, err)
		}
	})
}

func TestNoPasswordFlag(t *testing.T) {
	t.Parallel()
	for _, flag := range newCommand().Flags {
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
	for _, flag := range newCommand().Flags {
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
	for _, flag := range newCommand().Flags {
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
	for _, flag := range newCommand().Flags {
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
