package config

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Config is the validated runtime configuration passed to the exporter.
type Config struct {
	Endpoint           string
	BindAddress        string
	User               string
	Password           string
	CertFile           string
	KeyFile            string
	CACertFile         string
	ClientID           string
	Version            string
	InsecureSkipVerify bool
	// SysInterval sizes the $SYS/broker/uptime staleness-heartbeat window
	// only: while connected, the exporter treats the
	// broker as down once uptime has not advanced for 3*SysInterval. It is
	// not a per-topic expiry - every other cached series is reported
	// regardless of age while connected.
	SysInterval time.Duration
	// OTLPEndpoint is the OTLP receiver address. Empty (the default) means
	// OTLP export is disabled: no OTLP exporter is constructed and no OTLP
	// network egress happens. Prometheus scraping
	// via /metrics is unaffected either way.
	OTLPEndpoint string
	// OTLPProtocol selects the OTLP wire protocol, "grpc" or "http". Only
	// meaningful when OTLPEndpoint is set.
	OTLPProtocol string
	// OTLPInterval is the push interval for the OTLP PeriodicReader. Only
	// meaningful when OTLPEndpoint is set.
	OTLPInterval time.Duration
}

// Validate rejects unsupported or unsafe broker and telemetry settings.
// Passwords are accepted only from MQTT_PASS or --pass-file by the command
// package.
func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint: %w", err)
	}
	if u.Scheme == "" {
		return fmt.Errorf("endpoint must include a scheme, for example tcp:// or ssl://")
	}
	if !isSupportedMQTTScheme(u.Scheme) {
		return fmt.Errorf("unsupported MQTT endpoint scheme %q", u.Scheme)
	}
	scheme := strings.ToLower(u.Scheme)
	localTransport := scheme == "unix"
	if (localTransport && u.Host == "" && u.Path == "") || (!localTransport && u.Host == "") {
		return fmt.Errorf("broker endpoint is missing an address")
	}
	if u.User != nil {
		return fmt.Errorf("endpoint must not contain userinfo (user:pass@host); use --user/--pass-file or MQTT_USER/MQTT_PASS instead")
	}
	secureTransport := isSecureMQTTScheme(scheme)
	if c.Password != "" && c.User == "" {
		return fmt.Errorf("password requires a broker username")
	}
	if (c.User != "" || c.Password != "") && !secureTransport && !localTransport {
		return fmt.Errorf("broker credentials require a TLS endpoint (ssl://, tls://, mqtts://, mqtt+ssl://, tcps://, or wss://) or a local unix:// socket")
	}
	if (c.CertFile != "" || c.KeyFile != "" || c.CACertFile != "" || c.InsecureSkipVerify) && !secureTransport {
		return fmt.Errorf("TLS options require a TLS broker endpoint")
	}
	if c.SysInterval <= 0 {
		return fmt.Errorf("sys-interval must be positive, got %s", c.SysInterval)
	}
	// An empty OTLPEndpoint means OTLP export is disabled; protocol and
	// interval are then inert and must not fail validation regardless of
	// their value.
	if c.OTLPEndpoint != "" {
		if strings.Contains(c.OTLPEndpoint, "@") {
			return fmt.Errorf("otlp-endpoint must not contain credentials; use OTEL_EXPORTER_OTLP_HEADERS instead")
		}
		if c.OTLPProtocol != "grpc" && c.OTLPProtocol != "http" {
			return fmt.Errorf("otlp-protocol must be %q or %q, got %q", "grpc", "http", c.OTLPProtocol)
		}
		if c.OTLPInterval <= 0 {
			return fmt.Errorf("otlp-interval must be positive, got %s", c.OTLPInterval)
		}
	}
	return nil
}

func isSecureMQTTScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "ssl", "tls", "mqtts", "mqtt+ssl", "tcps", "wss":
		return true
	default:
		return false
	}
}

func isSupportedMQTTScheme(scheme string) bool {
	switch strings.ToLower(scheme) {
	case "mqtt", "tcp", "ws", "wss", "unix", "ssl", "tls", "mqtts", "mqtt+ssl", "tcps":
		return true
	default:
		return false
	}
}
