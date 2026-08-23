package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	yaml "go.yaml.in/yaml/v4"
)

// FileConfig holds the optional per-key overrides decoded from a YAML config
// file. A nil field means the key was absent from the
// file; the caller then falls through to its own default. Fields mirror the
// command's flag surface exactly - see cmd/mosquitto-exporter's flag
// definitions for the authoritative list and precedence order
// (flags > env > YAML > defaults).
type FileConfig struct {
	Endpoint           *string        `yaml:"endpoint"`
	BindAddress        *string        `yaml:"bind_address"`
	User               *string        `yaml:"user"`
	PassFile           *string        `yaml:"pass_file"`
	Cert               *string        `yaml:"cert"`
	Key                *string        `yaml:"key"`
	CACert             *string        `yaml:"ca_cert"`
	InsecureSkipVerify *bool          `yaml:"insecure_skip_verify"`
	ClientID           *string        `yaml:"client_id"`
	LogLevel           *string        `yaml:"log_level"`
	SysInterval        *time.Duration `yaml:"sys_interval"`
	OTLPEndpoint       *string        `yaml:"otlp_endpoint"`
	OTLPProtocol       *string        `yaml:"otlp_protocol"`
	OTLPInterval       *time.Duration `yaml:"otlp_interval"`
	// Password exists only to produce a useful rejection instead of an
	// unknown-field error. LoadFile never returns a config containing it.
	Password yaml.Node `yaml:"password"`
}

// ConfigFilePath resolves the --config/CONFIG_FILE value using the same
// flag > env precedence as every other setting, ahead of the full
// urfave/cli parse: the resolved path decides what LoadFile feeds in as
// YAML-layer flag defaults, so it must be known before the command's flags
// are built. An empty result means "no config file" - the file is optional
// and its absence at this unset default is not an error.
func ConfigFilePath(args []string, lookupEnv func(string) (string, bool)) string {
argsLoop:
	for i, arg := range args {
		switch {
		case arg == "--":
			break argsLoop
		case arg == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(arg, "--config="):
			return strings.TrimPrefix(arg, "--config=")
		}
	}
	if v, ok := lookupEnv("CONFIG_FILE"); ok {
		return v
	}
	return ""
}

// LoadFile reads and strictly decodes the YAML config file at path.
//
// An empty path means no file was named (the default) and returns a zero
// FileConfig with no error. A non-empty path was named explicitly, by flag
// or by CONFIG_FILE, so a missing or malformed file is a startup error -
// including an unknown key (strict decoding via KnownFields) and an inline
// "password" key, which YAML must never carry (it would put a secret in a
// file operators are encouraged to commit; use pass_file or MQTT_PASS).
func LoadFile(path string) (*FileConfig, error) {
	if path == "" {
		return &FileConfig{}, nil
	}

	//nolint:gosec // G304: path is the operator-supplied --config/CONFIG_FILE value
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening config file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	fc := &FileConfig{}
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(fc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("parsing config file %s: %w", path, err)
		}
		return nil, fmt.Errorf("config file %s must contain exactly one YAML document", path)
	}

	if fc.Password.Kind != 0 {
		return nil, fmt.Errorf("config file %s: %q is not a supported key - an inline password would risk being committed to version control; use %q (a path to a file containing the password) or the MQTT_PASS environment variable instead", path, "password", "pass_file")
	}
	return fc, nil
}
