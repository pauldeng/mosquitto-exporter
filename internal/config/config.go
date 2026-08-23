package config

import (
	"fmt"
	"net/url"
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
	// only (DESIGN.md §5.3): while connected, the exporter treats the
	// broker as down once uptime has not advanced for 3*SysInterval. It is
	// not a per-topic expiry - every other cached series is reported
	// regardless of age while connected.
	SysInterval time.Duration
}

// Validate rejects credentials embedded in the broker URL. Passwords are
// accepted only from MQTT_PASS or --pass-file by the command package.
func (c Config) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil {
		return fmt.Errorf("invalid endpoint %q: %w", c.Endpoint, err)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint must not contain userinfo (user:pass@host); use --user/--pass-file or MQTT_USER/MQTT_PASS instead")
	}
	if c.SysInterval <= 0 {
		return fmt.Errorf("sys-interval must be positive, got %s", c.SysInterval)
	}
	return nil
}
