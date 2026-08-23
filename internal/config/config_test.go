package config

import "testing"

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		endpoint string
		wantErr  bool
	}{
		{"plain tcp endpoint", "tcp://127.0.0.1:1883", false},
		{"ssl endpoint", "ssl://broker.example.com:8883", false},
		{"user and password in URL rejected", "tcp://user:pass@127.0.0.1:1883", true},
		{"bare username in URL rejected", "tcp://user@127.0.0.1:1883", true},
		{"unparseable URL", "://not-a-url", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (Config{Endpoint: tt.endpoint}).Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
