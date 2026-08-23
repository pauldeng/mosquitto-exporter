package config

import (
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name         string
		endpoint     string
		user         string
		password     string
		caCert       string
		sysInterval  time.Duration
		otlpEndpoint string
		otlpProtocol string
		otlpInterval time.Duration
		wantErr      bool
	}{
		{name: "plain tcp endpoint", endpoint: "tcp://127.0.0.1:1883", sysInterval: 10 * time.Second},
		{name: "ssl endpoint", endpoint: "ssl://broker.example.com:8883", sysInterval: 10 * time.Second},
		{name: "TLS credentials accepted", endpoint: "ssl://broker.example.com:8883", user: "alice", password: "secret", sysInterval: 10 * time.Second},
		{name: "unix socket credentials accepted", endpoint: "unix:///run/mosquitto.sock", user: "alice", password: "secret", sysInterval: 10 * time.Second},
		{name: "plaintext credentials rejected", endpoint: "tcp://broker.example.com:1883", user: "alice", password: "secret", sysInterval: 10 * time.Second, wantErr: true},
		{name: "password without username rejected", endpoint: "ssl://broker.example.com:8883", password: "secret", sysInterval: 10 * time.Second, wantErr: true},
		{name: "TLS options on plaintext endpoint rejected", endpoint: "tcp://broker.example.com:1883", caCert: "ca.pem", sysInterval: 10 * time.Second, wantErr: true},
		{name: "endpoint scheme is required", endpoint: "broker.example.com:1883", sysInterval: 10 * time.Second, wantErr: true},
		{name: "endpoint address is required", endpoint: "tcp://", sysInterval: 10 * time.Second, wantErr: true},
		{name: "unix socket path is required", endpoint: "unix://", sysInterval: 10 * time.Second, wantErr: true},
		{name: "user and password in URL rejected", endpoint: "tcp://user:pass@127.0.0.1:1883", sysInterval: 10 * time.Second, wantErr: true}, //nolint:gosec // G101: this is the malformed-input example the test asserts Validate() rejects, not a real credential
		{name: "bare username in URL rejected", endpoint: "tcp://user@127.0.0.1:1883", sysInterval: 10 * time.Second, wantErr: true},
		{name: "unparseable URL", endpoint: "://not-a-url", sysInterval: 10 * time.Second, wantErr: true},
		{name: "zero sys-interval rejected", endpoint: "tcp://127.0.0.1:1883", sysInterval: 0, wantErr: true},
		{name: "negative sys-interval rejected", endpoint: "tcp://127.0.0.1:1883", sysInterval: -time.Second, wantErr: true},
		{name: "sub-second sys-interval accepted (positive, just short)", endpoint: "tcp://127.0.0.1:1883", sysInterval: time.Millisecond},
		{
			name:        "otlp disabled by default: garbage protocol/interval are inert",
			endpoint:    "tcp://127.0.0.1:1883",
			sysInterval: 10 * time.Second,
			// OTLPEndpoint left empty on purpose; protocol/interval below
			// would fail validation if OTLP were enabled.
			otlpProtocol: "bogus",
			otlpInterval: 0,
		},
		{
			name:         "otlp enabled with grpc protocol accepted",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "localhost:4317",
			otlpProtocol: "grpc",
			otlpInterval: 60 * time.Second,
		},
		{
			name:         "otlp enabled with http protocol accepted",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "http://localhost:4318",
			otlpProtocol: "http",
			otlpInterval: 60 * time.Second,
		},
		{
			name:         "otlp enabled with unsupported protocol rejected",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "localhost:4317",
			otlpProtocol: "bogus",
			otlpInterval: 60 * time.Second,
			wantErr:      true,
		},
		{ //nolint:gosec // G101: malformed-input example proving credentials are rejected, not a real secret
			name:         "otlp endpoint credentials rejected",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "https://user:secret@collector.example.com:4318",
			otlpProtocol: "http",
			otlpInterval: 60 * time.Second,
			wantErr:      true,
		},
		{
			name:         "otlp enabled with zero interval rejected",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "localhost:4317",
			otlpProtocol: "grpc",
			otlpInterval: 0,
			wantErr:      true,
		},
		{
			name:         "otlp enabled with negative interval rejected",
			endpoint:     "tcp://127.0.0.1:1883",
			sysInterval:  10 * time.Second,
			otlpEndpoint: "localhost:4317",
			otlpProtocol: "grpc",
			otlpInterval: -time.Second,
			wantErr:      true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := (Config{
				Endpoint:     tt.endpoint,
				User:         tt.user,
				Password:     tt.password,
				CACertFile:   tt.caCert,
				SysInterval:  tt.sysInterval,
				OTLPEndpoint: tt.otlpEndpoint,
				OTLPProtocol: tt.otlpProtocol,
				OTLPInterval: tt.otlpInterval,
			}).Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
