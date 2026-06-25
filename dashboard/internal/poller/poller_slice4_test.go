package poller

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/wg"
)

// fakeWGRunner serves a canned `wg show <iface> dump` output and counts calls,
// so a test can assert the alert path reuses collect's single read rather than
// shelling out a second `sudo wg show`.
type fakeWGRunner struct {
	mu    sync.Mutex
	dump  string
	calls int
}

func (f *fakeWGRunner) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return []byte(f.dump), nil
}

func (f *fakeWGRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// dumpServerLine is the first line of `wg show dump` (the server's own info),
// which Show skips. Peer lines follow with 8 tab-separated fields:
// pubkey, psk, endpoint, allowed-ips, latest-handshake(unix), rx, tx, keepalive.
const dumpServerLine = "SRVPRIV\tSRVPUB\t51820\toff\n"

func peerDumpLine(pubkey string, handshakeUnix, rx, tx int64) string {
	return fmt.Sprintf("%s\t(none)\t1.2.3.4:51820\t10.0.0.2/32\t%d\t%d\t%d\t25\n",
		pubkey, handshakeUnix, rx, tx)
}

// manifestJSON renders a clients.json body for the given name→pubkey pairs.
func manifestJSON(t *testing.T, pairs ...[2]string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("[")
	for i, p := range pairs {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"name":%q,"address":"172.16.15.%d/32","public_key":%q}`, p[0], i+2, p[1])
	}
	b.WriteString("]")
	return b.String()
}

// newPeerPoller wires a Poller with a fake wg Runner + clientsfile Reader and a
// real (in-memory) DB so collect runs end-to-end and stashes the peer snapshot.
// proc/disk/service inputs are benign here — the focus is the peer conditions.
func newPeerPoller(t *testing.T, runner *fakeWGRunner, manifest string, ev *alerts.Evaluator, n *recordingNotifier, now func() time.Time) *Poller {
	t.Helper()
	database, err := db.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })

	wgSvc := wg.New()
	wgSvc.Runner = runner.run

	cf := clientsfile.New()
	cf.Reader = func(string) ([]byte, error) { return []byte(manifest), nil }

	procSvc := proc.New()
	procSvc.Reader = func(string) ([]byte, error) { return nil, fmt.Errorf("no proc") } // collect tolerates this

	return &Poller{
		DB:          database,
		Proc:        procSvc,
		WG:          wgSvc,
		ClientsFile: cf,
		Service:     &fakeService{active: []bool{true, true, true, true}},
		Evaluator:   ev,
		Notifier:    n,
		Now:         now,
		dispatch:    make(chan alerts.Event, dispatchBufferSize),
	}
}

func TestPoller_PeerDownDispatchedKeyedByName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clk }

	runner := &fakeWGRunner{}
	manifest := manifestJSON(t, [2]string{"alice", "ALICEPUB"})
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newPeerPoller(t, runner, manifest, ev, notifier, now)

	go p.drainDispatch(ctx)

	// Tick 1: alice online (fresh handshake) — latches seen-online, no event.
	runner.dump = dumpServerLine + peerDumpLine("ALICEPUB", clk.Add(-time.Minute).Unix(), 100, 100)
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	// Tick 2 (30m later): alice's handshake frozen → stale → peer-down fires.
	clk = clk.Add(30 * time.Minute)
	staleHS := clk.Add(-11 * time.Minute).Unix()
	runner.dump = dumpServerLine + peerDumpLine("ALICEPUB", staleHS, 100, 100)
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })
	if msg := notifier.snapshot()[0]; !containsAll(msg, "FIRING", "peer-down", "alice") {
		t.Fatalf("expected a peer-down fire naming alice, got %q", msg)
	}
}

func TestPoller_TransferCapDispatchedKeyedByName(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	now := func() time.Time { return clk }

	runner := &fakeWGRunner{}
	manifest := manifestJSON(t, [2]string{"bob", "BOBPUB"})
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host", TransferCapBytes: 50 << 30})
	p := newPeerPoller(t, runner, manifest, ev, notifier, now)

	go p.drainDispatch(ctx)

	// Tick 1: baseline at rx=0.
	runner.dump = dumpServerLine + peerDumpLine("BOBPUB", 0, 0, 0)
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	// Tick 2: rx jumps past the cap → transfer-cap fires.
	clk = clk.Add(time.Minute)
	runner.dump = dumpServerLine + peerDumpLine("BOBPUB", 0, 60<<30, 0)
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })
	if msg := notifier.snapshot()[0]; !containsAll(msg, "FIRING", "transfer-cap", "bob") {
		t.Fatalf("expected a transfer-cap fire naming bob, got %q", msg)
	}
}

func TestPoller_NonManifestPeerSkipped(t *testing.T) {
	ctx := context.Background()
	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	runner := &fakeWGRunner{}
	// Manifest knows alice only; the dump also carries a wg-only peer GHOSTPUB.
	manifest := manifestJSON(t, [2]string{"alice", "ALICEPUB"})
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newPeerPoller(t, runner, manifest, ev, &recordingNotifier{}, func() time.Time { return clk })

	runner.dump = dumpServerLine +
		peerDumpLine("ALICEPUB", clk.Add(-time.Minute).Unix(), 1, 1) +
		peerDumpLine("GHOSTPUB", clk.Add(-time.Minute).Unix(), 1, 1)
	_ = p.collect(ctx)

	p.peersMu.Lock()
	defer p.peersMu.Unlock()
	if !p.lastPeersValid {
		t.Fatalf("peer snapshot should be valid after a successful wg + manifest read")
	}
	if len(p.lastPeers) != 1 {
		t.Fatalf("non-manifest peer must be skipped: want 1 sample, got %d (%+v)", len(p.lastPeers), p.lastPeers)
	}
	if p.lastPeers[0].Name != "alice" {
		t.Fatalf("kept sample should be alice, got %q", p.lastPeers[0].Name)
	}
}

// TestPoller_WGShowOncePerTick is the reuse guard: a full tick (collect then
// evaluateAlerts) must call wg show exactly once. A second call would mean the
// alert path re-ran p.WG.Show instead of reusing collect's stashed snapshot.
func TestPoller_WGShowOncePerTick(t *testing.T) {
	ctx := context.Background()
	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	runner := &fakeWGRunner{dump: dumpServerLine + peerDumpLine("ALICEPUB", clk.Add(-time.Minute).Unix(), 1, 1)}
	manifest := manifestJSON(t, [2]string{"alice", "ALICEPUB"})
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newPeerPoller(t, runner, manifest, ev, &recordingNotifier{}, func() time.Time { return clk })

	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	if got := runner.callCount(); got != 1 {
		t.Fatalf("wg show called %d times in one tick, want exactly 1 (alert path must reuse collect's peers)", got)
	}
}

func TestPoller_NilEvaluatorNoPeerProcessing(t *testing.T) {
	ctx := context.Background()
	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	runner := &fakeWGRunner{dump: dumpServerLine + peerDumpLine("ALICEPUB", clk.Add(-time.Minute).Unix(), 1, 1)}
	manifest := manifestJSON(t, [2]string{"alice", "ALICEPUB"})
	p := newPeerPoller(t, runner, manifest, nil, &recordingNotifier{}, func() time.Time { return clk })
	p.Evaluator = nil

	// collect still runs (and stashes the snapshot) but evaluateAlerts is a no-op.
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	// No dispatch, no notify — evaluateAlerts returned immediately.
	if n := len(p.Notifier.(*recordingNotifier).snapshot()); n != 0 {
		t.Fatalf("nil evaluator should not notify, got %d", n)
	}
}
