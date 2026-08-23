package config

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name        string
		endpoint    string
		sysInterval time.Duration
		wantErr     bool
	}{
		{"plain tcp endpoint", "tcp://127.0.0.1:1883", 10 * time.Second, false},
		{"ssl endpoint", "ssl://broker.example.com:8883", 10 * time.Second, false},
		{"user and password in URL rejected", "tcp://user:pass@127.0.0.1:1883", 10 * time.Second, true},
		{"bare username in URL rejected", "tcp://user@127.0.0.1:1883", 10 * time.Second, true},
		{"unparseable URL", "://not-a-url", 10 * time.Second, true},
		{"zero sys-interval rejected", "tcp://127.0.0.1:1883", 0, true},
		{"negative sys-interval rejected", "tcp://127.0.0.1:1883", -time.Second, true},
		{"sub-second sys-interval accepted (positive, just short)", "tcp://127.0.0.1:1883", time.Millisecond, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (Config{Endpoint: tt.endpoint, SysInterval: tt.sysInterval}).Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
