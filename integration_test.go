//go:build integration

package main

// Docker-backed integration test (DESIGN.md §7 Phase 0 item 3, extended by
// Phase 3 item 8): starts the official broker image with a non-default
// sys_interval, connects the real exporter binary, drives enough real MQTT
// traffic to move every asserted counter off its initial value, and asserts
// the full Phase 3 (§6) exported series set.
//
// Run with: go test -tags=integration -run TestIntegrationSmoke -v ./...
// Skips cleanly if Docker isn't available, so it never fails on a laptop
// without it.
//
// Why "drive traffic, not just connect": $SYS values are change-gated
// (DESIGN.md §4) - metrics.next is only published when it differs from
// metrics.current. On a truly static value, a naive test could conclude a
// counter never gets exported at all.
//
// Empirically verified against a live eclipse-mosquitto:2.1.2-alpine
// container (2026-08-23) for this handoff: the broker's *first* sys_interval
// tick after any client connects (the exporter's own $SYS/# subscription
// counts) publishes a full baseline snapshot of every tracked metric,
// including ones that are still at their zero-valued default (observed
// directly for publish/messages/dropped, clients/expired,
// shared_subscriptions/count on a container with no prior client activity
// at all). Only later ticks are purely change-gated. In practice this means
// an always-zero counter still gets exported, because the exporter's own
// $SYS/# subscription is itself the "activity" that triggers that first
// snapshot - this test does not rely on that alone, though, and still
// drives real traffic through every counter that traffic can plausibly move
// (DESIGN.md §7 Phase 3 item 8's instruction), both for stronger assertions
// (some counters are checked non-zero, not just present) and as a safety net
// in case that first-tick behaviour is broker-version-specific.
//
// mosquitto_broker_bridge_connected is the one series excluded outright:
// it only exists once an admin has configured an actual bridge to another
// broker, which this single-broker harness does not stand up. See
// excludedSeries below for the recorded reason.

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// nonDefaultSysInterval is deliberately not the exporter's 10s default
// (DESIGN.md §7 Phase 3 item 6/8): it exercises --sys-interval end to end,
// on both the broker (so $SYS actually ticks this often) and the exporter
// (so its heartbeat window is sized to match).
const nonDefaultSysInterval = 3 * time.Second

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

// writeMosquittoConf writes a broker config overriding only sys_interval
// from the official image's own default (confirmed by inspecting the image:
// `listener 1883` + `allow_anonymous true`), so this test's broker still
// behaves like the plain smoke test's, just with a non-default tick rate.
func writeMosquittoConf(t *testing.T, sysInterval time.Duration) string {
	t.Helper()
	path := t.TempDir() + "/mosquitto.conf"
	conf := fmt.Sprintf("listener 1883\nallow_anonymous true\nsys_interval %d\n", int(sysInterval.Seconds()))
	//nolint:gosec // G306: broker config, no secrets, world-readable is fine
	if err := os.WriteFile(path, []byte(conf), 0o644); err != nil {
		t.Fatalf("writing mosquitto.conf: %v", err)
	}
	return path
}

// startBroker starts the official Mosquitto 2.1.2 image with confPath
// mounted over its default config (only sys_interval overridden - see
// writeMosquittoConf) and returns its host port.
func startBroker(t *testing.T, confPath string) int {
	t.Helper()
	brokerPort := freePort(t)

	//nolint:gosec // G204: fixed docker subcommand, brokerPort/confPath are locally chosen, not external input
	out, err := exec.Command("docker", "run", "-d",
		"-p", fmt.Sprintf("127.0.0.1:%d:1883", brokerPort),
		"-v", confPath+":/mosquitto/config/mosquitto.conf:ro",
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

// mustConnect connects an MQTT client, retrying for a while: a container
// that has just started accepting the raw TCP port does not always mean
// mosquitto's own accept loop is ready yet - the first MQTT CONNECT right
// after startBroker's TCP check can still see "connection reset by peer" for
// a brief window.
func mustConnect(t *testing.T, opts *mqtt.ClientOptions) mqtt.Client {
	t.Helper()
	var client mqtt.Client
	deadline := time.Now().Add(15 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		client = mqtt.NewClient(opts)
		token := client.Connect()
		if token.WaitTimeout(5*time.Second) && token.Error() == nil {
			return client
		}
		lastErr = token.Error()
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("client failed to connect to broker: %v", lastErr)
	return nil
}

// generateTraffic drives enough real MQTT activity to move every asserted
// $SYS counter off its initial value (DESIGN.md §7 Phase 3 item 8):
//   - a plain connect+publish+subscribe for the message/byte/packet counters
//     and connections/socket/count;
//   - a persistent-session (CleanSession=false) client that connects then
//     disconnects, for clients_disconnected/clients_total;
//   - a retained publish, for messages_stored/store_messages_bytes/retained_messages;
//   - a $share/ subscription held open across a full sys_interval tick, for
//     shared_subscriptions.
func generateTraffic(t *testing.T, brokerPort int, sysInterval time.Duration) {
	t.Helper()
	broker := fmt.Sprintf("tcp://127.0.0.1:%d", brokerPort)

	// Plain publish+subscribe: moves messages/publish counters and byte/packet
	// counts on both the receive and send side.
	sub := mustConnect(t, mqtt.NewClientOptions().AddBroker(broker).SetClientID("inttest-sub"))
	subDone := make(chan struct{})
	subToken := sub.Subscribe("integration-test/topic", 1, func(_ mqtt.Client, _ mqtt.Message) {
		select {
		case <-subDone:
		default:
			close(subDone)
		}
	})
	if !subToken.WaitTimeout(5*time.Second) || subToken.Error() != nil {
		t.Fatalf("test subscriber failed to subscribe: %v", subToken.Error())
	}

	pub := mustConnect(t, mqtt.NewClientOptions().AddBroker(broker).SetClientID("inttest-pub"))
	defer pub.Disconnect(250)
	pubToken := pub.Publish("integration-test/topic", 1, false, "hello")
	if !pubToken.WaitTimeout(5*time.Second) || pubToken.Error() != nil {
		t.Fatalf("test publisher failed to publish: %v", pubToken.Error())
	}
	select {
	case <-subDone:
	case <-time.After(5 * time.Second):
		t.Fatal("test subscriber never received the published message")
	}
	sub.Disconnect(250)

	// Retained publish: bumps messages_stored/store_messages_bytes/retained_messages
	// by an amount attributable to this test, beyond mosquitto's own $SYS
	// retained-topic baseline.
	retainer := mustConnect(t, mqtt.NewClientOptions().AddBroker(broker).SetClientID("inttest-retain"))
	defer retainer.Disconnect(250)
	retainToken := retainer.Publish("integration-test/retained", 1, true, "retained-hello")
	if !retainToken.WaitTimeout(5*time.Second) || retainToken.Error() != nil {
		t.Fatalf("test publisher failed to publish retained message: %v", retainToken.Error())
	}

	// Persistent session: connect with CleanSession=false, then disconnect
	// without ending the session, so the broker keeps it registered as
	// "disconnected but persistent" (clients_disconnected/clients_total,
	// including the deprecated clients/inactive alias).
	persistentOpts := mqtt.NewClientOptions().AddBroker(broker).SetClientID("inttest-persistent").SetCleanSession(false)
	persistent := mustConnect(t, persistentOpts)
	persistent.Disconnect(250)

	// Shared subscription: held open across at least one full sys_interval
	// tick so the broker publishes shared_subscriptions/count > 0 at least
	// once while it is active. It does not need to still be connected at
	// scrape time - the value is retained (DESIGN.md §4), so whatever was
	// last published (0 or 1) is what a later subscriber receives; the point
	// here is only to make sure a change away from the unpublished/zero
	// state actually happens.
	shareDone := make(chan struct{})
	shareOpts := mqtt.NewClientOptions().AddBroker(broker).SetClientID("inttest-shared")
	shareClient := mustConnect(t, shareOpts)
	shareToken := shareClient.Subscribe("$share/inttest-group/shared/topic", 0, nil)
	if !shareToken.WaitTimeout(5*time.Second) || shareToken.Error() != nil {
		t.Fatalf("test shared-subscription client failed to subscribe: %v", shareToken.Error())
	}
	go func() {
		time.Sleep(2 * sysInterval)
		shareClient.Disconnect(250)
		close(shareDone)
	}()
	t.Cleanup(func() { <-shareDone })
}

// expectedSeries is the full Phase 3 (DESIGN.md §6) exported metric family
// set this test asserts present in /metrics after driving traffic, minus
// excludedSeries.
var expectedSeries = []string{
	// Counters (§6.1).
	"mosquitto_broker_messages_received_total",
	"mosquitto_broker_messages_sent_total",
	"mosquitto_broker_bytes_received_total",
	"mosquitto_broker_bytes_sent_total",
	"mosquitto_broker_publish_bytes_received_total",
	"mosquitto_broker_publish_bytes_sent_total",
	"mosquitto_broker_publish_messages_received_total",
	"mosquitto_broker_publish_messages_sent_total",
	"mosquitto_broker_publish_messages_dropped_total",
	"mosquitto_broker_connections_socket_total",
	"mosquitto_broker_clients_expired_total",
	// Gauges (§6.2).
	"mosquitto_broker_clients_total",
	"mosquitto_broker_clients_connected",
	"mosquitto_broker_clients_disconnected",
	"mosquitto_broker_clients_maximum",
	"mosquitto_broker_messages_stored",
	"mosquitto_broker_store_messages_bytes",
	"mosquitto_broker_subscriptions",
	"mosquitto_broker_shared_subscriptions",
	"mosquitto_broker_retained_messages",
	"mosquitto_broker_packet_out_count",
	"mosquitto_broker_packet_out_bytes",
	"mosquitto_broker_heap_current_bytes",
	"mosquitto_broker_heap_maximum_bytes",
	"mosquitto_broker_uptime_seconds",
	// Shape changes (§6.3).
	"mosquitto_broker_load_connections",
	"mosquitto_broker_info",
	// Exporter self-metrics.
	"mosquitto_up",
	"mosquitto_exporter_invalid_payloads_total",
	"mosquitto_exporter_unknown_topics_total",
	"mosquitto_exporter_dropped_topics_total",
}

// excludedSeries lists every DESIGN.md §6 series deliberately not asserted,
// with the reason it cannot be produced from this harness (DESIGN.md §7
// Phase 3 item 8).
var excludedSeries = map[string]string{
	"mosquitto_broker_bridge_connected": "requires an admin-configured bridge to a second broker (DESIGN.md §6.3); " +
		"this single-broker harness never publishes $SYS/broker/connection/+/state at all, so no traffic pattern " +
		"can produce it. The topic->label mapping itself is unit-tested in internal/sys.",
}

func TestIntegrationSmoke(t *testing.T) {
	t.Parallel()
	requireDocker(t)

	confPath := writeMosquittoConf(t, nonDefaultSysInterval)
	brokerPort := startBroker(t, confPath)
	generateTraffic(t, brokerPort, nonDefaultSysInterval)

	binPath := t.TempDir() + "/mosquitto-exporter"
	//nolint:gosec // G204: fixed "go build" subcommand, binPath is our own t.TempDir() path
	build := exec.Command("go", "build", "-o", binPath, "./cmd/mosquitto-exporter")
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
		"--sys-interval", nonDefaultSysInterval.String(),
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting exporter: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	metricsURL := fmt.Sprintf("http://127.0.0.1:%d/metrics", bindPort)

	// Poll until every expected series has appeared, or time out. Generous
	// deadline: several sys_interval ticks plus the 2x sys_interval the
	// shared-subscription client stays connected for.
	const pollFor = 45 * time.Second
	start := time.Now()
	deadline := start.Add(pollFor)
	var body string
	var missing []string
	for time.Now().Before(deadline) {
		b, err := scrapeOnce(metricsURL)
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		body = b
		missing = missingSeries(body, expectedSeries)
		if len(missing) == 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if len(missing) > 0 {
		t.Fatalf("missing expected series after %s: %v\nexcluded (not asserted): %v\nlast scraped body:\n%s",
			time.Since(start), missing, excludedNames(), body)
	}

	if !strings.Contains(body, "mosquitto_up 1") {
		t.Errorf("want mosquitto_up 1 once connected and ticking; body:\n%s", body)
	}

	// A handful of the driven counters should be attributably non-zero, not
	// merely present - a stronger check than presence alone for the ones
	// this harness directly moves (beyond the $SYS tree's own baseline
	// chatter, which alone would leave the always-zero counters in
	// expectedSeries at 0 - see the empirical note in this file's header).
	for _, name := range []string{
		"mosquitto_broker_messages_received_total",
		"mosquitto_broker_publish_messages_received_total",
		"mosquitto_broker_clients_total",
	} {
		value, ok := metricValue(body, name)
		if !ok {
			t.Errorf("%s: no unlabelled sample line found in body", name)
			continue
		}
		if value == "0" {
			t.Errorf("%s want non-zero after driven traffic, got 0", name)
		}
	}

	t.Logf("saw the full expected Phase 3 series set on port %d after driving traffic (sys_interval=%s); excluded: %v",
		brokerPort, nonDefaultSysInterval, excludedNames())
}

// missingSeries returns the subset of names whose "# TYPE <name> " line is
// not present in body.
func missingSeries(body string, names []string) []string {
	var missing []string
	for _, name := range names {
		if !strings.Contains(body, "# TYPE "+name+" ") {
			missing = append(missing, name)
		}
	}
	return missing
}

func excludedNames() []string {
	names := make([]string, 0, len(excludedSeries))
	for name, reason := range excludedSeries {
		names = append(names, name+": "+reason)
	}
	return names
}

// metricValue returns the value of body's unlabelled sample line for name
// ("<name> <value>", no "{...}" label suffix), e.g. for
// "mosquitto_broker_clients_total 3" it returns ("3", true).
func metricValue(body, name string) (string, bool) {
	for line := range strings.SplitSeq(body, "\n") {
		if value, ok := strings.CutPrefix(line, name+" "); ok {
			return value, true
		}
	}
	return "", false
}
