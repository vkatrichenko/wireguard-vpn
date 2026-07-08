package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// sampleMetricsBody mirrors the real exposition shape asserted by
// dashboard/internal/server/server_test.go's TestHandleGetMetricsProm: HELP/
// TYPE comment lines, a peer with an escaped double-quote in its name and a
// known handshake ("evil\"peer"), a peer that has never handshaked and so
// has no age series ("bob"), two disk mounts (one is "/", a lexicographic
// prefix of the other — exercises the sort), and one metric family
// (wireguard_future_metric_totally_new) this parser has never heard of, to
// prove unknown families are ignored rather than fatal.
const sampleMetricsBody = `# HELP wireguard_service_active WireGuard wg-quick@wg0 systemd unit active (1) or inactive (0).
# TYPE wireguard_service_active gauge
wireguard_service_active 1
# HELP wireguard_peers_total Total WireGuard peers in the manifest.
# TYPE wireguard_peers_total gauge
wireguard_peers_total 2
# HELP wireguard_peers_online Peers whose most recent handshake is within the online window.
# TYPE wireguard_peers_online gauge
wireguard_peers_online 1
# HELP wireguard_peer_last_handshake_age_seconds Seconds since each peer's most recent handshake.
# TYPE wireguard_peer_last_handshake_age_seconds gauge
wireguard_peer_last_handshake_age_seconds{peer="evil\"peer"} 60
# HELP wireguard_peer_rx_bytes_total Cumulative bytes received from each peer.
# TYPE wireguard_peer_rx_bytes_total counter
wireguard_peer_rx_bytes_total{peer="bob"} 5
wireguard_peer_rx_bytes_total{peer="evil\"peer"} 100
# HELP wireguard_peer_tx_bytes_total Cumulative bytes transmitted to each peer.
# TYPE wireguard_peer_tx_bytes_total counter
wireguard_peer_tx_bytes_total{peer="bob"} 6
wireguard_peer_tx_bytes_total{peer="evil\"peer"} 200
# HELP wireguard_host_cpu_percent Host CPU utilisation percent.
# TYPE wireguard_host_cpu_percent gauge
wireguard_host_cpu_percent 12.5
# HELP wireguard_host_memory_percent Host memory utilisation percent.
# TYPE wireguard_host_memory_percent gauge
wireguard_host_memory_percent 40
# HELP wireguard_host_disk_percent Filesystem fullness percent per mount.
# TYPE wireguard_host_disk_percent gauge
wireguard_host_disk_percent{mount="/"} 73.2
wireguard_host_disk_percent{mount="/data"} 12.1
# HELP wireguard_active_alerts Number of currently-firing alerts.
# TYPE wireguard_active_alerts gauge
wireguard_active_alerts 3
# HELP wireguard_future_metric_totally_new A metric family this parser predates.
# TYPE wireguard_future_metric_totally_new gauge
wireguard_future_metric_totally_new{foo="bar"} 42
# HELP wireguard_build_info Build metadata; value is always 1, version/sha carried as labels.
# TYPE wireguard_build_info gauge
wireguard_build_info{version="v1.2.3",sha="deadbeef"} 1
`

// TestParseHostMetrics exercises the parser directly against sampleMetricsBody
// — the fastest, most direct check that every documented metric family maps
// onto the right struct field, that the unknown family is silently ignored,
// and that the never-handshaked peer's age is nil rather than a fabricated 0.
func TestParseHostMetrics(t *testing.T) {
	got := parseHostMetrics(sampleMetricsBody)

	if got.ServiceActive == nil || !*got.ServiceActive {
		t.Fatalf("ServiceActive = %v, want true", got.ServiceActive)
	}
	if got.PeersTotal != 2 {
		t.Errorf("PeersTotal = %d, want 2", got.PeersTotal)
	}
	if got.PeersOnline != 1 {
		t.Errorf("PeersOnline = %d, want 1", got.PeersOnline)
	}
	if got.CPUPercent == nil || *got.CPUPercent != 12.5 {
		t.Errorf("CPUPercent = %v, want 12.5", got.CPUPercent)
	}
	if got.MemoryPercent == nil || *got.MemoryPercent != 40 {
		t.Errorf("MemoryPercent = %v, want 40", got.MemoryPercent)
	}
	if got.ActiveAlerts != 3 {
		t.Errorf("ActiveAlerts = %d, want 3", got.ActiveAlerts)
	}
	if got.BuildVersion != "v1.2.3" || got.BuildSHA != "deadbeef" {
		t.Errorf("build info = %q/%q, want v1.2.3/deadbeef", got.BuildVersion, got.BuildSHA)
	}

	if len(got.Disks) != 2 {
		t.Fatalf("len(Disks) = %d, want 2: %+v", len(got.Disks), got.Disks)
	}
	// Sorted by mount: "/" sorts before "/data" (a lexicographic prefix).
	if got.Disks[0].Mount != "/" || got.Disks[0].PercentFull != 73.2 {
		t.Errorf("Disks[0] = %+v, want {/ 73.2}", got.Disks[0])
	}
	if got.Disks[1].Mount != "/data" || got.Disks[1].PercentFull != 12.1 {
		t.Errorf("Disks[1] = %+v, want {/data 12.1}", got.Disks[1])
	}

	if len(got.Peers) != 2 {
		t.Fatalf("len(Peers) = %d, want 2: %+v", len(got.Peers), got.Peers)
	}
	// Sorted by name: "bob" < `evil"peer`.
	bob := got.Peers[0]
	if bob.Name != "bob" || bob.RxBytes != 5 || bob.TxBytes != 6 {
		t.Errorf("Peers[0] = %+v, want {bob 5 6 <nil>}", bob)
	}
	if bob.LastHandshakeAgeSeconds != nil {
		t.Errorf("bob.LastHandshakeAgeSeconds = %v, want nil (never handshaked)", *bob.LastHandshakeAgeSeconds)
	}
	evil := got.Peers[1]
	if evil.Name != `evil"peer` || evil.RxBytes != 100 || evil.TxBytes != 200 {
		t.Errorf("Peers[1] = %+v, want {evil\"peer 100 200 60}", evil)
	}
	if evil.LastHandshakeAgeSeconds == nil || *evil.LastHandshakeAgeSeconds != 60 {
		t.Errorf("evil.LastHandshakeAgeSeconds = %v, want 60", evil.LastHandshakeAgeSeconds)
	}

	if got.Raw != sampleMetricsBody {
		t.Errorf("Raw does not match the input body verbatim")
	}
}

// TestParseHostMetricsTolerant proves malformed/unrecognized lines are
// skipped rather than fatal: an unterminated label block, a label without a
// closing quote, a line with no value, and a blank line, interleaved with
// one valid sample that must still parse correctly.
func TestParseHostMetricsTolerant(t *testing.T) {
	body := strings.Join([]string{
		"wireguard_peers_total 5",
		`wireguard_broken_labels{peer="unterminated`,
		"wireguard_no_value",
		"",
		`wireguard_host_disk_percent{mount="/"} 50`,
	}, "\n")

	got := parseHostMetrics(body)
	if got.PeersTotal != 5 {
		t.Errorf("PeersTotal = %d, want 5 (parse should continue past malformed lines)", got.PeersTotal)
	}
	if len(got.Disks) != 1 || got.Disks[0].Mount != "/" || got.Disks[0].PercentFull != 50 {
		t.Errorf("Disks = %+v, want [{/ 50}]", got.Disks)
	}
}

// newHostMetricsTestServer wires an httptest server that serves body as the
// Prometheus text exposition at GET /metrics only (any other path 404s, so a
// test accidentally hitting the wrong path fails loudly instead of silently
// getting the metrics body back).
func newHostMetricsTestServer(t *testing.T, body string) (*dashboard.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(body))
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	return dashboard.New(addr), srv.Close
}

// TestGetHostMetricsToolReturnsStructuredContent is the end-to-end path:
// real Register wiring, a fake dashboard serving the sibling /metrics path
// (not /api/*), and an assertion against the tool's StructuredContent — the
// SDK-populated structured-output field AddTool fills automatically because
// this handler's Out type is hostMetrics, not `any` (unlike every other
// tool in tools.go, which proxies raw /api/* JSON through as text instead).
func TestGetHostMetricsToolReturnsStructuredContent(t *testing.T) {
	client, closeSrv := newHostMetricsTestServer(t, sampleMetricsBody)
	defer closeSrv()
	call := newReadOnlyTestServer(t, client)

	res := call("get_host_metrics", map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}
	if res.StructuredContent == nil {
		t.Fatalf("StructuredContent is nil; want the parsed hostMetrics JSON")
	}

	// StructuredContent is `any` on the wire type (mcp.CallToolResult); after
	// a round trip through the in-memory transport's JSON encoding it may
	// already be a map[string]any rather than the json.RawMessage the server
	// side originally set, so re-marshal defensively before decoding into
	// hostMetrics instead of assuming a concrete type.
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshalling StructuredContent: %v", err)
	}
	var got hostMetrics
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshalling StructuredContent: %v", err)
	}

	if got.PeersTotal != 2 || got.PeersOnline != 1 {
		t.Errorf("peers_total/online = %d/%d, want 2/1", got.PeersTotal, got.PeersOnline)
	}
	if got.ActiveAlerts != 3 {
		t.Errorf("ActiveAlerts = %d, want 3", got.ActiveAlerts)
	}
	if len(got.Disks) != 2 {
		t.Fatalf("len(Disks) = %d, want 2", len(got.Disks))
	}
	if len(got.Peers) != 2 {
		t.Fatalf("len(Peers) = %d, want 2", len(got.Peers))
	}
	if got.BuildVersion != "v1.2.3" || got.BuildSHA != "deadbeef" {
		t.Errorf("build info = %q/%q, want v1.2.3/deadbeef", got.BuildVersion, got.BuildSHA)
	}
}

// TestGetHostMetricsToolPropagatesStatusError proves a non-2xx /metrics
// response surfaces as an error result, exactly like every other read-only
// tool's failure path (tools_test.go's sibling tests cover the /api/* case;
// this is the /metrics-specific instance of the same dashboard.StatusError
// contract).
func TestGetHostMetricsToolPropagatesStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	client := dashboard.New(strings.TrimPrefix(srv.URL, "http://"))
	call := newReadOnlyTestServer(t, client)

	res := call("get_host_metrics", map[string]any{})
	if !res.IsError {
		t.Fatalf("expected an error result for a 500 from /metrics, got: %+v", res)
	}
}
