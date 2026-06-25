package poller

import (
	"context"
	"testing"
	"time"

	"wireguard-dashboard/internal/alerts"
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
