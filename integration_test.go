//go:build integration

package main

// Minimal Docker-backed smoke test (DESIGN.md §7 Phase 0 item 3): start the
// official broker image, connect the real exporter binary, generate a little
// traffic, and assert /metrics carries a real $SYS-derived sample. Phase 3
// extends this to the full 56-topic inventory; this one stays deliberately
// small.
//
// Run with: go test -tags=integration -run TestIntegrationSmoke -v .
// Skips cleanly if Docker isn't available, so it never fails on a laptop
// without it.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// scrapeOnce fetches the given URL and returns its body as a string.
func scrapeOnce(url string) (string, error) {
	//nolint:gosec // G107: url is always our own http://127.0.0.1:<port>/metrics, built locally, never external input
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH, skipping integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable, skipping integration test")
	}
}

// freePort asks the OS for an unused TCP port on 127.0.0.1.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// startBroker starts the official Mosquitto 2.1.2 image (default config
// already allows anonymous access on 1883) and returns its host port.
// The broker is started FIRST because the exporter today does not serve
// /metrics until it has connected (defect R1, DESIGN.md §3.3).
func startBroker(t *testing.T) int {
	t.Helper()
	brokerPort := freePort(t)

	//nolint:gosec // G204: fixed docker subcommand, brokerPort is a locally chosen int, not external input
	out, err := exec.Command("docker", "run", "-d",
		"-p", fmt.Sprintf("127.0.0.1:%d:1883", brokerPort),
		"eclipse-mosquitto:2.1.2-alpine",
	).Output()
	if err != nil {
		t.Fatalf("starting mosquitto container: %v", err)
	}
	containerID := strings.TrimSpace(string(out))
	t.Cleanup(func() {
		//nolint:gosec // G204: containerID comes from our own `docker run` output above, not external input
		_ = exec.Command("docker", "rm", "-f", containerID).Run()
	})

	// Wait for the broker to accept TCP connections.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", brokerPort), time.Second)
		if err == nil {
			_ = conn.Close()
			return brokerPort
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("mosquitto broker on port %d never accepted a connection", brokerPort)
	return 0
}

// generateTraffic connects a throwaway MQTT client and publishes one
// message, so at least one non-uptime $SYS counter moves off zero.
//
// Connect is retried: a container that has just started accepting the raw
// TCP port does not always mean mosquitto's own accept loop is ready yet -
// the first MQTT CONNECT right after startBroker's TCP check can still see
// "connection reset by peer" for a brief window.
func generateTraffic(t *testing.T, brokerPort int) {
	t.Helper()
	opts := mqtt.NewClientOptions().AddBroker(fmt.Sprintf("tcp://127.0.0.1:%d", brokerPort))

	var client mqtt.Client
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client = mqtt.NewClient(opts)
		token := client.Connect()
		if token.WaitTimeout(5*time.Second) && token.Error() == nil {
			lastErr = nil
			break
		}
		lastErr = token.Error()
		time.Sleep(300 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("test client failed to connect to broker: %v", lastErr)
	}
	defer client.Disconnect(250)

	pubToken := client.Publish("integration-test/topic", 0, false, "hello")
	if !pubToken.WaitTimeout(5*time.Second) || pubToken.Error() != nil {
		t.Fatalf("test client failed to publish: %v", pubToken.Error())
	}
}

func TestIntegrationSmoke(t *testing.T) {
	t.Parallel()
	requireDocker(t)

	brokerPort := startBroker(t)
	generateTraffic(t, brokerPort)

	binPath := t.TempDir() + "/mosquitto-exporter"
	//nolint:gosec // G204: fixed "go build" subcommand, binPath is our own t.TempDir() path
	build := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building exporter: %v\n%s", err, out)
	}

	bindPort := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	//nolint:gosec // G204: binPath is the binary we just built above, not external input
	cmd := exec.CommandContext(ctx, binPath,
		"--endpoint", fmt.Sprintf("tcp://127.0.0.1:%d", brokerPort),
		"--bind-address", fmt.Sprintf("127.0.0.1:%d", bindPort),
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting exporter: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", bindPort)
	deadline := time.Now().Add(30 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		b, err := scrapeOnce(metricsURL)
		if err == nil {
			body = b
			// $SYS/broker/uptime is the one topic Mosquitto publishes
			// unconditionally every tick (DESIGN.md §4), so it's the most
			// reliable real $SYS-derived sample to wait for.
			if strings.Contains(body, "broker_uptime") {
				break
			}
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !strings.Contains(body, "broker_uptime") {
		t.Fatalf("/metrics never showed a $SYS-derived sample (broker_uptime) within the deadline; last body:\n%s", body)
	}
	t.Logf("saw $SYS-derived sample in /metrics after connecting to the broker on port %d", brokerPort)
}
