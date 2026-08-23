package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConfigFilePath(t *testing.T) {
	t.Parallel()
	noEnv := func(string) (string, bool) { return "", false }
	envFunc := func(value string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			if key == "CONFIG_FILE" {
				return value, true
			}
			return "", false
		}
	}

	for _, tt := range []struct {
		name string
		args []string
		env  func(string) (string, bool)
		want string
	}{
		{name: "nothing set", args: nil, env: noEnv, want: ""},
		{name: "--config=path form", args: []string{"--config=/etc/foo.yaml"}, env: noEnv, want: "/etc/foo.yaml"},
		{name: "--config path form", args: []string{"--config", "/etc/foo.yaml"}, env: noEnv, want: "/etc/foo.yaml"},
		{name: "--config with no following value falls through to env", args: []string{"--config"}, env: envFunc("/env/foo.yaml"), want: "/env/foo.yaml"},
		{name: "env only", args: nil, env: envFunc("/env/foo.yaml"), want: "/env/foo.yaml"},
		{name: "flag wins over env", args: []string{"--config", "/flag/foo.yaml"}, env: envFunc("/env/foo.yaml"), want: "/flag/foo.yaml"},
		{name: "other flags ignored", args: []string{"--endpoint", "tcp://x:1883", "--config=/etc/foo.yaml"}, env: noEnv, want: "/etc/foo.yaml"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := ConfigFilePath(tt.args, tt.env); got != tt.want {
				t.Errorf("ConfigFilePath(%v) = %q, want %q", tt.args, got, tt.want)
			}
		})
	}
}

func writeYAML(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadFile(t *testing.T) {
	t.Parallel()

	t.Run("empty path means no file - not an error, no fields set", func(t *testing.T) {
		t.Parallel()
		fc, err := LoadFile("")
		if err != nil {
			t.Fatalf("LoadFile(\"\") error = %v", err)
		}
		if fc.Endpoint != nil || fc.SysInterval != nil {
			t.Errorf("LoadFile(\"\") = %+v, want all-nil FileConfig", fc)
		}
	})

	t.Run("explicit path missing is a startup error", func(t *testing.T) {
		t.Parallel()
		_, err := LoadFile(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
		if err == nil {
			t.Fatal("expected an error for a missing, explicitly named config file")
		}
	})

	t.Run("comment-only file parses to zero FileConfig", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "# nothing configured yet\n")
		fc, err := LoadFile(path)
		if err != nil {
			t.Fatalf("LoadFile() error = %v", err)
		}
		if fc.Endpoint != nil {
			t.Errorf("fc.Endpoint = %v, want nil", *fc.Endpoint)
		}
	})

	t.Run("recognised keys decode onto the right fields", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, `
endpoint: tcp://broker.example.com:1883
bind_address: 127.0.0.1:9234
user: metrics
pass_file: /etc/mosquitto-exporter/pass
cert: /etc/mosquitto-exporter/cert.pem
key: /etc/mosquitto-exporter/key.pem
ca_cert: /etc/mosquitto-exporter/ca.pem
insecure_skip_verify: true
client_id: exporter-1
log_level: debug
sys_interval: 15s
otlp_endpoint: localhost:4317
otlp_protocol: grpc
otlp_interval: 30s
`)
		fc, err := LoadFile(path)
		if err != nil {
			t.Fatalf("LoadFile() error = %v", err)
		}
		if got, want := *fc.Endpoint, "tcp://broker.example.com:1883"; got != want {
			t.Errorf("Endpoint = %q, want %q", got, want)
		}
		if got, want := *fc.BindAddress, "127.0.0.1:9234"; got != want {
			t.Errorf("BindAddress = %q, want %q", got, want)
		}
		if got, want := *fc.User, "metrics"; got != want {
			t.Errorf("User = %q, want %q", got, want)
		}
		if got, want := *fc.PassFile, "/etc/mosquitto-exporter/pass"; got != want {
			t.Errorf("PassFile = %q, want %q", got, want)
		}
		if got, want := *fc.Cert, "/etc/mosquitto-exporter/cert.pem"; got != want {
			t.Errorf("Cert = %q, want %q", got, want)
		}
		if got, want := *fc.Key, "/etc/mosquitto-exporter/key.pem"; got != want {
			t.Errorf("Key = %q, want %q", got, want)
		}
		if got, want := *fc.CACert, "/etc/mosquitto-exporter/ca.pem"; got != want {
			t.Errorf("CACert = %q, want %q", got, want)
		}
		if fc.InsecureSkipVerify == nil || !*fc.InsecureSkipVerify {
			t.Errorf("InsecureSkipVerify = %v, want true", fc.InsecureSkipVerify)
		}
		if got, want := *fc.ClientID, "exporter-1"; got != want {
			t.Errorf("ClientID = %q, want %q", got, want)
		}
		if got, want := *fc.LogLevel, "debug"; got != want {
			t.Errorf("LogLevel = %q, want %q", got, want)
		}
		if fc.SysInterval == nil || *fc.SysInterval != 15*time.Second {
			t.Errorf("SysInterval = %v, want 15s", fc.SysInterval)
		}
		if got, want := *fc.OTLPEndpoint, "localhost:4317"; got != want {
			t.Errorf("OTLPEndpoint = %q, want %q", got, want)
		}
		if got, want := *fc.OTLPProtocol, "grpc"; got != want {
			t.Errorf("OTLPProtocol = %q, want %q", got, want)
		}
		if fc.OTLPInterval == nil || *fc.OTLPInterval != 30*time.Second {
			t.Errorf("OTLPInterval = %v, want 30s", fc.OTLPInterval)
		}
	})

	t.Run("unknown key is a clear startup error", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "endpiont: tcp://typo:1883\n")
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("expected an error for an unknown YAML key")
		}
	})

	t.Run("malformed YAML syntax is a startup error", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "endpoint: [unterminated\n")
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("expected an error for malformed YAML")
		}
	})

	t.Run("inline password key is rejected with a clear error", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "password: hunter2\n")
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("expected an error for an inline password key")
		}
		if got := err.Error(); !strings.Contains(got, "pass_file") {
			t.Errorf("error = %q, want it to point at pass_file", got)
		}
	})

	t.Run("invalid sys_interval is a startup error", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "sys_interval: not-a-duration\n")
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("expected an error for an invalid sys_interval")
		}
	})

	t.Run("invalid otlp_interval is a startup error", func(t *testing.T) {
		t.Parallel()
		path := writeYAML(t, "otlp_interval: not-a-duration\n")
		_, err := LoadFile(path)
		if err == nil {
			t.Fatal("expected an error for an invalid otlp_interval")
		}
	})
}

// TestExampleYAMLParses guards mosquitto-exporter.example.yaml against rot
// (DESIGN.md §7 Phase 5 item 7): every key it shows must be one this
// strict, single-Decode-call-site loader actually accepts.
func TestExampleYAMLParses(t *testing.T) {
	t.Parallel()
	fc, err := LoadFile("../../mosquitto-exporter.example.yaml")
	if err != nil {
		t.Fatalf("LoadFile(mosquitto-exporter.example.yaml) error = %v", err)
	}
	if fc == nil {
		t.Fatal("LoadFile returned a nil FileConfig")
	}
}
