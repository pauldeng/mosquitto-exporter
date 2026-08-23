package config

import (
	"fmt"
	"net/url"
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
	ResetMetrics       bool
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
	return nil
}
