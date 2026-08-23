package main

import (
	"os"
	"path/filepath"
	"testing"
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
