package alerts

import (
	"strings"
	"testing"
	"time"
)

// fsAt builds a one-filesystem Input for the high-disk condition.
func fsAt(mount string, pct float64) Input {
	return Input{ServiceActive: true, DiskUsage: []FilesystemUsage{{Mount: mount, PctFull: pct}}}
}

// onlyEvent asserts Evaluate returned exactly one event for the given condition
// and Kind, and returns it. Service is held active in these tests so the
// service-down condition never contributes an event.
func onlyEvent(t *testing.T, evs []Event, cond Condition, want Kind) Event {
	t.Helper()
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Condition != cond {
		t.Fatalf("want condition %q, got %q", cond, evs[0].Condition)
	}
	if evs[0].Kind != want {
		t.Fatalf("want kind %v, got %v", want, evs[0].Kind)
	}
	return evs[0]
}

func TestHighDisk_BoundaryFires(t *testing.T) {
	// Just under the default 90% threshold: OK.
	e := New(Config{Host: "h"})
	if evs := e.Evaluate(base, fsAt("/", 89.9)); len(evs) != 0 {
		t.Fatalf("89.9%% should be below threshold, got %+v", evs)
	}

	// Exactly at the threshold (>= is inclusive): fire once.
	e2 := New(Config{Host: "h"})
	ev := onlyEvent(t, e2.Evaluate(base, fsAt("/", 90.0)), ConditionHighDisk, Fire)
	if !strings.Contains(ev.Detail, "/") || !strings.Contains(ev.Detail, "90.0") {
		t.Fatalf("detail should name mount and percent, got %q", ev.Detail)
	}
}

func TestHighDisk_DetailNamesFullestMount(t *testing.T) {
	e := New(Config{Host: "h"})
	in := Input{
		ServiceActive: true,
		DiskUsage: []FilesystemUsage{
			{Mount: "/", PctFull: 91.2},
			{Mount: "/data", PctFull: 97.5},
			{Mount: "/boot", PctFull: 50.0},
		},
	}
	ev := onlyEvent(t, e.Evaluate(base, in), ConditionHighDisk, Fire)
	if !strings.Contains(ev.Detail, "/data") || !strings.Contains(ev.Detail, "97.5") {
		t.Fatalf("detail should name the fullest offending mount, got %q", ev.Detail)
	}
}

func TestHighDisk_RecoversWhenAllBelow(t *testing.T) {
	e := New(Config{Host: "h"})
	onlyEvent(t, e.Evaluate(base, fsAt("/", 95.0)), ConditionHighDisk, Fire)

	// All filesystems drop below threshold: one Recovery.
	onlyEvent(t, e.Evaluate(base.Add(time.Minute), fsAt("/", 80.0)), ConditionHighDisk, Recovery)

	// Still below: nothing.
	if evs := e.Evaluate(base.Add(2*time.Minute), fsAt("/", 80.0)); len(evs) != 0 {
		t.Fatalf("second below tick should emit nothing, got %+v", evs)
	}
}

func TestHighDisk_NilDiskUsageIsOK(t *testing.T) {
	// A tick whose disk read failed (DiskUsage nil) must not fire.
	e := New(Config{Host: "h"})
	if evs := e.Evaluate(base, Input{ServiceActive: true}); len(evs) != 0 {
		t.Fatalf("nil disk usage should not fire, got %+v", evs)
	}
}

func TestHighDisk_CooldownSuppressesRefire(t *testing.T) {
	e := New(Config{Host: "h", Cooldown: 30 * time.Minute})
	onlyEvent(t, e.Evaluate(base, fsAt("/", 95.0)), ConditionHighDisk, Fire)

	// Still over but within cooldown: suppressed.
	if evs := e.Evaluate(base.Add(10*time.Minute), fsAt("/", 96.0)); len(evs) != 0 {
		t.Fatalf("re-notify within cooldown should suppress, got %+v", evs)
	}

	// At the cooldown boundary: re-notify.
	onlyEvent(t, e.Evaluate(base.Add(30*time.Minute), fsAt("/", 96.0)), ConditionHighDisk, Fire)
}

// cpuHigh / cpuLow are convenience Inputs for the sustained-CPU condition with
// the service held active so only CPU can contribute events.
func cpuHigh(pct float64) Input { return Input{ServiceActive: true, CPUPercent: pct} }

func TestHighCPU_SpikeIgnored(t *testing.T) {
	// One tick at/over threshold then a drop must NEVER fire — the sustain run
	// resets the instant CPU falls below.
	e := New(Config{Host: "h"})
	if evs := e.Evaluate(base, cpuHigh(99)); len(evs) != 0 {
		t.Fatalf("first high tick should not fire (sustain not met), got %+v", evs)
	}
	if evs := e.Evaluate(base.Add(10*time.Second), cpuHigh(5)); len(evs) != 0 {
		t.Fatalf("drop after spike should not fire, got %+v", evs)
	}
	// Even far past the window, since the run reset, no fire.
	if evs := e.Evaluate(base.Add(10*time.Minute), cpuHigh(5)); len(evs) != 0 {
		t.Fatalf("spike must never fire, got %+v", evs)
	}
}

func TestHighCPU_SustainedFiresAtBoundary(t *testing.T) {
	e := New(Config{Host: "h", CPUSustain: 5 * time.Minute})

	// First crossing arms the timer at base; not yet sustained.
	if evs := e.Evaluate(base, cpuHigh(96)); len(evs) != 0 {
		t.Fatalf("first high tick should not fire, got %+v", evs)
	}
	// Just under the window: still suppressed.
	justUnder := base.Add(5*time.Minute - time.Second)
	if evs := e.Evaluate(justUnder, cpuHigh(96)); len(evs) != 0 {
		t.Fatalf("just-under-window should still suppress, got %+v", evs)
	}
	// Exactly at the window (>= inclusive): fire once.
	ev := onlyEvent(t, e.Evaluate(base.Add(5*time.Minute), cpuHigh(96)), ConditionHighCPU, Fire)
	if !strings.Contains(ev.Detail, "96") {
		t.Fatalf("detail should carry the CPU percent, got %q", ev.Detail)
	}
}

func TestHighCPU_DropMidWindowResets(t *testing.T) {
	e := New(Config{Host: "h", CPUSustain: 5 * time.Minute})

	// Arm at base, hold high for a few minutes (not yet sustained).
	e.Evaluate(base, cpuHigh(96))
	if evs := e.Evaluate(base.Add(3*time.Minute), cpuHigh(96)); len(evs) != 0 {
		t.Fatalf("mid-window should not fire, got %+v", evs)
	}

	// Drop below mid-window: resets the run.
	if evs := e.Evaluate(base.Add(4*time.Minute), cpuHigh(10)); len(evs) != 0 {
		t.Fatalf("drop should not fire, got %+v", evs)
	}

	// Climb high again: a NEW run starts here, so 5 min after the ORIGINAL
	// crossing is NOT enough — must wait a fresh full window.
	e.Evaluate(base.Add(5*time.Minute), cpuHigh(96))
	if evs := e.Evaluate(base.Add(6*time.Minute), cpuHigh(96)); len(evs) != 0 {
		t.Fatalf("new run must restart the window, got %+v", evs)
	}
	// A full window after the SECOND crossing: fires.
	onlyEvent(t, e.Evaluate(base.Add(10*time.Minute), cpuHigh(96)), ConditionHighCPU, Fire)
}

func TestHighCPU_RecoversAfterSustainedFire(t *testing.T) {
	e := New(Config{Host: "h", CPUSustain: 5 * time.Minute})

	e.Evaluate(base, cpuHigh(96))
	onlyEvent(t, e.Evaluate(base.Add(5*time.Minute), cpuHigh(96)), ConditionHighCPU, Fire)

	// CPU drops below threshold: one Recovery.
	onlyEvent(t, e.Evaluate(base.Add(6*time.Minute), cpuHigh(20)), ConditionHighCPU, Recovery)

	// Still low: nothing.
	if evs := e.Evaluate(base.Add(7*time.Minute), cpuHigh(20)); len(evs) != 0 {
		t.Fatalf("second low tick should emit nothing, got %+v", evs)
	}
}

func TestHighCPU_FirstSampleZeroDoesNotArm(t *testing.T) {
	// The known proc first-sample CPUPercent==0 is below threshold, so it must
	// keep the condition OK and not arm the sustain timer.
	e := New(Config{Host: "h", CPUSustain: 5 * time.Minute})
	if evs := e.Evaluate(base, cpuHigh(0)); len(evs) != 0 {
		t.Fatalf("zero first-sample should not fire, got %+v", evs)
	}
	if !e.cpuHighSince.IsZero() {
		t.Fatalf("zero first-sample should not arm the sustain timer")
	}
}

// TestConditionIsolation proves a firing disk alert does not change CPU/service
// state and vice-versa: the three conditions key independent state machines.
func TestConditionIsolation(t *testing.T) {
	e := New(Config{Host: "h", CPUSustain: 5 * time.Minute})

	// Disk over, CPU below, service up: exactly one disk Fire.
	in := Input{ServiceActive: true, CPUPercent: 10, DiskUsage: []FilesystemUsage{{Mount: "/", PctFull: 95}}}
	onlyEvent(t, e.Evaluate(base, in), ConditionHighDisk, Fire)

	// Now drive CPU sustained while disk stays over. Disk is suppressed (within
	// cooldown), so the only NEW event after the window is the CPU Fire.
	high := Input{ServiceActive: true, CPUPercent: 99, DiskUsage: []FilesystemUsage{{Mount: "/", PctFull: 95}}}
	if evs := e.Evaluate(base.Add(time.Minute), high); len(evs) != 0 {
		t.Fatalf("CPU not yet sustained, disk in cooldown: expected nothing, got %+v", evs)
	}
	onlyEvent(t, e.Evaluate(base.Add(6*time.Minute), high), ConditionHighCPU, Fire)
}

// TestThresholdDefaultsApplied proves zero-valued Config knobs fall back to the
// exported defaults.
func TestThresholdDefaultsApplied(t *testing.T) {
	e := New(Config{Host: "h"})
	if e.diskThresholdPct != DefaultDiskThresholdPct {
		t.Fatalf("disk threshold default = %v, want %v", e.diskThresholdPct, DefaultDiskThresholdPct)
	}
	if e.cpuThresholdPct != DefaultCPUThresholdPct {
		t.Fatalf("cpu threshold default = %v, want %v", e.cpuThresholdPct, DefaultCPUThresholdPct)
	}
	if e.cpuSustain != DefaultCPUSustain {
		t.Fatalf("cpu sustain default = %v, want %v", e.cpuSustain, DefaultCPUSustain)
	}
}
