package poller

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"wireguard-dashboard/internal/alerts"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/disk"
	"wireguard-dashboard/internal/proc"
	"wireguard-dashboard/internal/wg"
)

// fakeDisk is a DiskReader returning canned mounts (or an error). Records call
// count so a test can confirm the poller does (or does NOT) read disk.
type fakeDisk struct {
	mu     sync.Mutex
	mounts []disk.Mount
	err    error
	calls  int
}

func (f *fakeDisk) Sample(ctx context.Context) ([]disk.Mount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.mounts, f.err
}

func (f *fakeDisk) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestEvaluateAlerts_HighDiskDispatched(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	svc := &fakeService{active: []bool{true, true}}
	dsk := &fakeDisk{mounts: []disk.Mount{{Path: "/", PctFull: 50}, {Path: "/data", PctFull: 96.4}}}
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })
	p.Disk = dsk

	go p.drainDispatch(ctx)

	p.evaluateAlerts(ctx)
	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })

	msg := notifier.snapshot()[0]
	if !containsAll(msg, "FIRING", "high-disk", "/data", "96.4") {
		t.Fatalf("first message not a high-disk fire naming the fullest mount: %q", msg)
	}
}

func TestEvaluateAlerts_DiskErrorDoesNotBreakOtherConditions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	// Service down on this tick so service-down fires even though disk errors.
	svc := &fakeService{active: []bool{false}}
	dsk := &fakeDisk{err: errors.New("statfs boom")}
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host"})
	p := newAlertPoller(svc, ev, notifier, func() time.Time { return now })
	p.Disk = dsk

	go p.drainDispatch(ctx)

	p.evaluateAlerts(ctx)
	// The disk read failed, but service-down must still evaluate and dispatch.
	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })
	if msg := notifier.snapshot()[0]; !strings.Contains(msg, "service-down") {
		t.Fatalf("tick should continue past disk error and fire service-down, got %q", msg)
	}
	if dsk.callCount() != 1 {
		t.Fatalf("disk should have been read once, got %d", dsk.callCount())
	}
}

func TestEvaluateAlerts_NilEvaluatorSkipsDiskRead(t *testing.T) {
	dsk := &fakeDisk{mounts: []disk.Mount{{Path: "/", PctFull: 99}}}
	p := &Poller{Disk: dsk, Now: time.Now}

	p.evaluateAlerts(context.Background())

	if dsk.callCount() != 0 {
		t.Fatalf("nil evaluator must not read disk, got %d calls", dsk.callCount())
	}
}

// countingProc builds a *proc.Service backed by a synthetic /proc + /sys served
// from memory, counting how many times /proc/stat is read. proc.Service reads
// /proc/stat exactly once per Sample, so the stat-read count IS the Sample
// count — the assertion that guards against a double-sample in the alert path.
//
// statValue lets successive ticks return different CPU jiffies so the delta is
// non-zero and CPUPercent is computable, exercising the real reuse path.
type countingProc struct {
	mu        sync.Mutex
	statReads int
	statTotal uint64 // user jiffies, advanced per Sample to vary the delta
}

func (c *countingProc) read(path string) ([]byte, error) {
	switch {
	case strings.HasSuffix(path, "/stat"):
		c.mu.Lock()
		c.statReads++
		total := c.statTotal
		c.statTotal += 1000 // advance so the next sample sees a delta
		c.mu.Unlock()
		// cpu  user nice system idle iowait irq softirq steal guest guest_nice
		return []byte(fmt.Sprintf("cpu  %d 0 0 100 0 0 0 0 0 0\n", total)), nil
	case strings.HasSuffix(path, "/meminfo"):
		return []byte("MemTotal:       1000 kB\nMemAvailable:    400 kB\n"), nil
	case strings.HasSuffix(path, "/uptime"):
		return []byte("12345.67 9999.00\n"), nil
	case strings.HasSuffix(path, "rx_bytes"), strings.HasSuffix(path, "tx_bytes"):
		return []byte("1000\n"), nil
	}
	return nil, fmt.Errorf("countingProc: unexpected read %q", path)
}

func (c *countingProc) reads() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.statReads
}

// TestProcSampledOncePerTick is the no-double-sample guard: a full tick (collect
// then evaluateAlerts) must read /proc/stat exactly once. A second read would
// mean the alert path called proc.Sample again, splitting the per-tick CPU delta
// and corrupting both the chart row and the alert reading.
func TestProcSampledOncePerTick(t *testing.T) {
	ctx := context.Background()

	database, err := db.Open(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = database.Close() }()

	cp := &countingProc{}
	procSvc := proc.New()
	procSvc.Reader = cp.read
	procSvc.ProcPath = "/proc"
	procSvc.SysPath = "/sys"

	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	dsk := &fakeDisk{mounts: []disk.Mount{{Path: "/", PctFull: 10}}}
	ev := alerts.New(alerts.Config{Host: "test-host"})

	p := &Poller{
		DB:          database,
		Proc:        procSvc,
		WG:          wg.New(),          // will fail (no sudo) — collect is best-effort
		ClientsFile: clientsfile.New(), // will fail (no file) — collect is best-effort
		Service:     &fakeService{active: []bool{true}},
		Disk:        dsk,
		Evaluator:   ev,
		Notifier:    &recordingNotifier{},
		Now:         func() time.Time { return now },
		dispatch:    make(chan alerts.Event, dispatchBufferSize),
	}

	// One full tick: collect (samples proc once) then evaluateAlerts (must NOT
	// sample proc again — it reuses collect's cached CPU%).
	_ = p.collect(ctx)
	p.evaluateAlerts(ctx)

	if got := cp.reads(); got != 1 {
		t.Fatalf("proc /proc/stat read %d times in one tick, want exactly 1 (double-sample would corrupt the CPU delta)", got)
	}

	// The CPU% collect computed must have been stashed for the alert path.
	p.statsMu.Lock()
	valid := p.lastCPUValid
	p.statsMu.Unlock()
	if !valid {
		t.Fatalf("collect should have stashed a valid CPU%% for the alert path to reuse")
	}
}

// TestEvaluateAlerts_HighCPUFromCollectStats proves the sustained-CPU condition
// fires off the CPU% that collect stashed — driven entirely through the cached
// reading, never a re-sample in the alert path.
func TestEvaluateAlerts_HighCPUFromCollectStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clk := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	notifier := &recordingNotifier{}
	ev := alerts.New(alerts.Config{Host: "test-host", CPUSustain: 5 * time.Minute})
	p := newAlertPoller(&fakeService{active: []bool{true, true}}, ev, notifier, func() time.Time { return clk })

	go p.drainDispatch(ctx)

	// Simulate collect stashing a high CPU reading, then arm + sustain.
	p.statsMu.Lock()
	p.lastCPUPct = 97
	p.lastCPUValid = true
	p.statsMu.Unlock()

	p.evaluateAlerts(ctx) // arms the sustain timer at clk; no fire yet
	clk = clk.Add(5 * time.Minute)
	p.evaluateAlerts(ctx) // window elapsed → fire

	waitFor(t, func() bool { return len(notifier.snapshot()) == 1 })
	if msg := notifier.snapshot()[0]; !containsAll(msg, "FIRING", "high-cpu", "97") {
		t.Fatalf("expected a sustained high-cpu fire, got %q", msg)
	}
}
