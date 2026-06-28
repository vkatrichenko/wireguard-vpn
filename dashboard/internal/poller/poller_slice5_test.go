package poller

import (
	"context"
	"testing"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/disk"
)

// TestEvaluateAlerts_UpdatesStatusHolder asserts the poller feeds the in-UI
// status holder each tick: a firing condition shows up in Active and the
// transition lands in Recent, then a recovery clears Active.
func TestEvaluateAlerts_UpdatesStatusHolder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{false, true}}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	holder := alerts.NewStatusHolder()
	holder.SetEnabled(true)

	p := newAlertPoller(svc, ev, &recordingNotifier{}, func() time.Time { return now })
	p.StatusHolder = holder

	go p.drainDispatch(ctx)

	// Tick 1: service down → Active has service-down, Recent has one Fire.
	p.evaluateAlerts(ctx)
	snap := holder.Snapshot()
	if !snap.Enabled {
		t.Fatalf("holder should report enabled")
	}
	if len(snap.Active) != 1 || snap.Active[0].Key != "service-down" {
		t.Fatalf("tick1: want service-down active, got %+v", snap.Active)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].Kind != alerts.Fire {
		t.Fatalf("tick1: want one Fire in Recent, got %+v", snap.Recent)
	}

	// Tick 2: service up → Active empties, Recent gains a Recovery (newest-first).
	p.evaluateAlerts(ctx)
	snap = holder.Snapshot()
	if len(snap.Active) != 0 {
		t.Fatalf("tick2: Active should be empty after recovery, got %+v", snap.Active)
	}
	if len(snap.Recent) != 2 || snap.Recent[0].Kind != alerts.Recovery {
		t.Fatalf("tick2: want Recovery newest-first, got %+v", snap.Recent)
	}
}

// TestEvaluateAlerts_NilHolderNoPanic confirms a nil StatusHolder is tolerated
// (back-compat): evaluation + dispatch proceed without touching the holder.
func TestEvaluateAlerts_NilHolderNoPanic(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{false}}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	notifier := &recordingNotifier{}
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })
	// p.StatusHolder stays nil.

	go p.drainDispatch(ctx)
	p.evaluateAlerts(ctx) // must not panic
	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })
}

// TestMetricsSnapshot_PeersFromCollect drives a full collect with two manifest
// peers — one with a recent handshake (online), one stale (offline) — and
// asserts the Prometheus metrics snapshot reflects total/online counts and the
// per-peer byte counters, all derived from collect's single wg read (no second
// Show). Proc fails in newPeerPoller, so CPU/Mem stay Not-Known here.
func TestMetricsSnapshot_PeersFromCollect(t *testing.T) {
	ctx := context.Background()
	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	runner := &fakeWGRunner{}
	manifest := manifestJSON(t, [2]string{"alice", "ALICEPUB"}, [2]string{"bob", "BOBPUB"})
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newPeerPoller(t, runner, manifest, ev, &recordingNotifier{}, func() time.Time { return clk })

	// alice handshaked 1m ago → online; bob 10m ago → offline.
	runner.dump = dumpServerLine +
		peerDumpLine("ALICEPUB", clk.Add(-time.Minute).Unix(), 100, 200) +
		peerDumpLine("BOBPUB", clk.Add(-10*time.Minute).Unix(), 5, 6)
	_ = p.collect(ctx)

	snap := p.MetricsSnapshot()
	if snap.PeersTotal != 2 {
		t.Fatalf("PeersTotal = %d, want 2", snap.PeersTotal)
	}
	if snap.PeersOnline != 1 {
		t.Fatalf("PeersOnline = %d, want 1 (only alice within the 3m window)", snap.PeersOnline)
	}
	if len(snap.Peers) != 2 {
		t.Fatalf("Peers len = %d, want 2", len(snap.Peers))
	}
	var alice *PeerMetric
	for i := range snap.Peers {
		if snap.Peers[i].Name == "alice" {
			alice = &snap.Peers[i]
		}
	}
	if alice == nil {
		t.Fatalf("alice missing from peer metrics: %+v", snap.Peers)
	}
	if alice.RxBytes != 100 || alice.TxBytes != 200 {
		t.Fatalf("alice rx/tx = %d/%d, want 100/200", alice.RxBytes, alice.TxBytes)
	}
	if snap.CPUKnown || snap.MemKnown {
		t.Fatalf("host CPU/Mem should be Not-Known when proc fails, got CPUKnown=%v MemKnown=%v", snap.CPUKnown, snap.MemKnown)
	}
}

// TestMetricsSnapshot_HostServiceDisks exercises the snapshot accumulators
// directly (both run on the sample goroutine in production) and asserts the
// host gauges, service flag, and disk list land in the snapshot. It also proves
// MetricsSnapshot returns a deep copy: mutating the returned slice does not
// corrupt the next read.
func TestMetricsSnapshot_HostServiceDisks(t *testing.T) {
	p := &Poller{Now: time.Now}

	// Before any read: nothing is Known, counts are zero.
	if snap := p.MetricsSnapshot(); snap.CPUKnown || snap.MemKnown || snap.ServiceKnown {
		t.Fatalf("fresh poller should report nothing Known, got %+v", snap)
	}

	p.recordHostMetrics(true, 12.5, 40.0)
	p.recordServiceMetric(true)
	p.recordDiskMetrics([]disk.Mount{
		{Path: "/", PctFull: 73.2},
		{Path: "/data", PctFull: 12.0},
	})

	snap := p.MetricsSnapshot()
	if !snap.CPUKnown || snap.CPUPercent != 12.5 {
		t.Fatalf("CPU = %v (known=%v), want 12.5 known", snap.CPUPercent, snap.CPUKnown)
	}
	if !snap.MemKnown || snap.MemPercent != 40.0 {
		t.Fatalf("Mem = %v (known=%v), want 40.0 known", snap.MemPercent, snap.MemKnown)
	}
	if !snap.ServiceKnown || !snap.ServiceActive {
		t.Fatalf("Service known=%v active=%v, want known+active", snap.ServiceKnown, snap.ServiceActive)
	}
	if len(snap.Disks) != 2 || snap.Disks[0].Mount != "/" || snap.Disks[0].PctFull != 73.2 {
		t.Fatalf("Disks = %+v, want / at 73.2 first", snap.Disks)
	}

	// Deep-copy guard: corrupting the returned slice must not leak into state.
	snap.Disks[0].Mount = "CORRUPT"
	if again := p.MetricsSnapshot(); again.Disks[0].Mount != "/" {
		t.Fatalf("MetricsSnapshot must deep-copy Disks; got %q after caller mutation", again.Disks[0].Mount)
	}

	// A subsequent failed proc sample flips the host gauges back to Not-Known.
	p.recordHostMetrics(false, 0, 0)
	if snap := p.MetricsSnapshot(); snap.CPUKnown || snap.MemKnown {
		t.Fatalf("after a failed proc sample CPU/Mem should be Not-Known, got %+v", snap)
	}
}
