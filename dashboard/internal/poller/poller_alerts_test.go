package poller

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/systemd"
)

// fakeService is a ServiceReader returning canned statuses in sequence; once the
// script is exhausted it repeats the last entry. Records how many times Get was
// called so a test can confirm the poller actually read it each tick.
type fakeService struct {
	mu     sync.Mutex
	active []bool
	calls  int
}

func (f *fakeService) Get(ctx context.Context) (systemd.ServiceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := f.calls
	if idx >= len(f.active) {
		idx = len(f.active) - 1
	}
	f.calls++
	return systemd.ServiceStatus{Active: f.active[idx], State: stateOf(f.active[idx])}, nil
}

func stateOf(active bool) string {
	if active {
		return "active"
	}
	return "inactive"
}

// recordingNotifier captures every delivered message. An optional block channel
// lets a test make Notify hang to prove delivery is off the critical path; an
// optional err makes every call fail to prove a failing notifier never breaks
// the tick.
type recordingNotifier struct {
	mu       sync.Mutex
	messages []string
	block    chan struct{}
	err      error
}

func (n *recordingNotifier) Notify(ctx context.Context, message string) error {
	if n.block != nil {
		select {
		case <-n.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	n.mu.Lock()
	n.messages = append(n.messages, message)
	n.mu.Unlock()
	return n.err
}

func (n *recordingNotifier) snapshot() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]string, len(n.messages))
	copy(out, n.messages)
	return out
}

// newAlertPoller builds a Poller wired ONLY for alert evaluation: nil data deps
// (DB/Proc/WG/ClientsFile) are fine because we never call collect — these tests
// drive evaluateAlerts directly. A fixed clock makes the cooldown deterministic.
func newAlertPoller(svc ServiceReader, ev *alerts.Evaluator, n *recordingNotifier, now func() time.Time) *Poller {
	return &Poller{
		Service:   svc,
		Evaluator: ev,
		Notifier:  n,
		Now:       now,
		dispatch:  make(chan alerts.Event, dispatchBufferSize),
	}
}

// drainDispatch runs the dispatch worker until ctx cancellation, mirroring what
// Run does, so tests can exercise evaluate→dispatch→notify end to end without
// standing up the full Run loop.
func (p *Poller) drainDispatch(ctx context.Context) { p.runDispatchLoop(ctx) }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestEvaluateAlerts_FireThenRecoveryDispatched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{false, true}}
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })

	go p.drainDispatch(ctx)

	// Tick 1: service down → one Fire dispatched.
	p.evaluateAlerts(ctx)
	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })

	// Tick 2: service back up → one Recovery dispatched.
	p.evaluateAlerts(ctx)
	waitFor(t, func() bool { return len(notifier.snapshot()) == 2 })

	msgs := notifier.snapshot()
	if !containsAll(msgs[0], "FIRING", "service-down", "test-host") {
		t.Fatalf("first message not a service-down fire: %q", msgs[0])
	}
	if !containsAll(msgs[1], "RECOVERED", "service-down", "test-host") {
		t.Fatalf("second message not a recovery: %q", msgs[1])
	}
}

func TestEvaluateAlerts_SlowNotifierDoesNotBlockTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{false}}
	// A notifier that blocks forever until released. If evaluateAlerts called
	// Notify synchronously, the tick below would hang and the test would
	// time out.
	notifier := &recordingNotifier{block: make(chan struct{})}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })

	go p.drainDispatch(ctx)

	done := make(chan struct{})
	go func() {
		// Many ticks while the single in-flight Notify is wedged. These must
		// all return promptly — the buffered channel absorbs events and the
		// blocked worker never back-pressures the sample path.
		for i := 0; i < dispatchBufferSize+10; i++ {
			p.evaluateAlerts(ctx)
		}
		close(done)
	}()

	select {
	case <-done:
		// Tick path completed despite the wedged notifier — the point of the test.
	case <-time.After(2 * time.Second):
		t.Fatal("evaluateAlerts blocked on a slow notifier")
	}

	// The worker is still parked inside Notify, so nothing delivered yet.
	if got := len(notifier.snapshot()); got != 0 {
		t.Fatalf("notifier should be blocked, but delivered %d messages", got)
	}

	// Release it; the single buffered fire eventually delivers.
	close(notifier.block)
	waitFor(t, func() bool { return len(notifier.snapshot()) >= 1 })
}

func TestEvaluateAlerts_ErroringNotifierDoesNotBreakTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{false, true}}
	notifier := &recordingNotifier{err: context.DeadlineExceeded}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })

	go p.drainDispatch(ctx)

	// Both ticks must complete and both events still reach Notify (the worker
	// logs the error and moves on); the erroring return never propagates back.
	p.evaluateAlerts(ctx)
	p.evaluateAlerts(ctx)
	waitFor(t, func() bool { return len(notifier.snapshot()) == 2 })
}

func TestEvaluateAlerts_NilEvaluatorIsNoOp(t *testing.T) {
	// With Evaluator nil the poller must not touch the systemd reader or the
	// notifier — behaviour identical to pre-alerting.
	svc := &fakeService{active: []bool{false}}
	notifier := &recordingNotifier{}
	p := &Poller{Service: svc, Notifier: notifier, Now: time.Now}

	p.evaluateAlerts(context.Background())

	if svc.calls != 0 {
		t.Fatalf("nil evaluator should not read systemd, but Get was called %d times", svc.calls)
	}
	if len(notifier.snapshot()) != 0 {
		t.Fatalf("nil evaluator should not notify")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
