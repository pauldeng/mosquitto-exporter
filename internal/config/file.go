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
// file (DESIGN.md §7 Phase 5). A nil field means the key was absent from the
// file; the caller then falls through to its own default. Fields mirror the
// command's flag surface exactly - see cmd/mosquitto-exporter's flag
// definitions for the authoritative list and precedence order
// (flags > env > YAML > defaults).
type FileConfig struct {
	Endpoint           *string
	BindAddress        *string
	User               *string
	PassFile           *string
	Cert               *string
	Key                *string
	CACert             *string
	InsecureSkipVerify *bool
	ClientID           *string
	LogLevel           *string
	SysInterval        *time.Duration
	OTLPEndpoint       *string
	OTLPProtocol       *string
	OTLPInterval       *time.Duration
}

// rawFileConfig is the exact, strictly-decoded shape of the YAML document.
// Duration fields are decoded as strings and parsed separately (below) so
// that go.yaml.in/yaml/v4 is invoked from exactly one call site (§9.11): a
// custom yaml.Unmarshaler on a duration type would add a second one.
//
// Password is declared here - rather than left to trigger the generic
// unknown-field error - so LoadFile can reject it with a message that
// explains why, instead of a bare "field not found".
type rawFileConfig struct {
	Endpoint           *string `yaml:"endpoint"`
	BindAddress        *string `yaml:"bind_address"`
	User               *string `yaml:"user"`
	PassFile           *string `yaml:"pass_file"`
	Cert               *string `yaml:"cert"`
	Key                *string `yaml:"key"`
	CACert             *string `yaml:"ca_cert"`
	InsecureSkipVerify *bool   `yaml:"insecure_skip_verify"`
	ClientID           *string `yaml:"client_id"`
	LogLevel           *string `yaml:"log_level"`
	SysInterval        *string `yaml:"sys_interval"`
	OTLPEndpoint       *string `yaml:"otlp_endpoint"`
	OTLPProtocol       *string `yaml:"otlp_protocol"`
	OTLPInterval       *string `yaml:"otlp_interval"`
	Password           *string `yaml:"password"`
}

// ConfigFilePath resolves the --config/CONFIG_FILE value using the same
// flag > env precedence as every other setting, ahead of the full
// urfave/cli parse: the resolved path decides what LoadFile feeds in as
// YAML-layer flag defaults, so it must be known before the command's flags
// are built. An empty result means "no config file" - the file is optional
// and its absence at this unset default is not an error.
func ConfigFilePath(args []string, lookupEnv func(string) (string, bool)) string {
	for i, arg := range args {
		switch {
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

	var raw rawFileConfig
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("parsing config file %s: %w", path, err)
	}

	if raw.Password != nil {
		return nil, fmt.Errorf("config file %s: %q is not a supported key - an inline password would risk being committed to version control; use %q (a path to a file containing the password) or the MQTT_PASS environment variable instead", path, "password", "pass_file")
	}

	fc := &FileConfig{
		Endpoint:           raw.Endpoint,
		BindAddress:        raw.BindAddress,
		User:               raw.User,
		PassFile:           raw.PassFile,
		Cert:               raw.Cert,
		Key:                raw.Key,
		CACert:             raw.CACert,
		InsecureSkipVerify: raw.InsecureSkipVerify,
		ClientID:           raw.ClientID,
		LogLevel:           raw.LogLevel,
		OTLPEndpoint:       raw.OTLPEndpoint,
		OTLPProtocol:       raw.OTLPProtocol,
	}

	if fc.SysInterval, err = parseDurationField(path, "sys_interval", raw.SysInterval); err != nil {
		return nil, err
	}
	if fc.OTLPInterval, err = parseDurationField(path, "otlp_interval", raw.OTLPInterval); err != nil {
		return nil, err
	}
	return fc, nil
}

func parseDurationField(path, key string, raw *string) (*time.Duration, error) {
	if raw == nil {
		return nil, nil
	}
	d, err := time.ParseDuration(*raw)
	if err != nil {
		return nil, fmt.Errorf("config file %s: invalid %s %q: %w", path, key, *raw, err)
	}
	return &d, nil
}
