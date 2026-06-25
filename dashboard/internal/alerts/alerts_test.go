package alerts

import (
	"strings"
	"testing"
	"time"
)

// base is a fixed anchor so every clock arithmetic in the tests is explicit.
var base = time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

// down/up are convenience Inputs for the service-down condition.
var (
	down = Input{ServiceActive: false}
	up   = Input{ServiceActive: true}
)

// singleEvent asserts Evaluate returned exactly one event of the given Kind for
// the service-down condition and returns it.
func singleEvent(t *testing.T, evs []Event, want Kind) Event {
	t.Helper()
	if len(evs) != 1 {
		t.Fatalf("want exactly 1 event, got %d: %+v", len(evs), evs)
	}
	if evs[0].Kind != want {
		t.Fatalf("want kind %v, got %v", want, evs[0].Kind)
	}
	if evs[0].Condition != ConditionServiceDown {
		t.Fatalf("want condition %q, got %q", ConditionServiceDown, evs[0].Condition)
	}
	return evs[0]
}

func TestServiceDown_FiresOnceOnEntry(t *testing.T) {
	e := New(Config{Host: "h"})

	// OK + good: no event.
	if evs := e.Evaluate(base, up); len(evs) != 0 {
		t.Fatalf("ok+good should emit nothing, got %+v", evs)
	}

	// OK → FIRING: exactly one Fire.
	ev := singleEvent(t, e.Evaluate(base.Add(30*time.Second), down), Fire)
	if ev.At != base.Add(30*time.Second) {
		t.Fatalf("event timestamp = %v, want %v", ev.At, base.Add(30*time.Second))
	}

	// Still firing, well within cooldown: suppressed.
	if evs := e.Evaluate(base.Add(time.Minute), down); len(evs) != 0 {
		t.Fatalf("re-notification within cooldown should be suppressed, got %+v", evs)
	}
}

func TestServiceDown_CooldownBoundary(t *testing.T) {
	e := New(Config{Host: "h", Cooldown: 30 * time.Minute})

	// Fire at base.
	singleEvent(t, e.Evaluate(base, down), Fire)

	// Just under the 30-min cooldown: suppressed.
	justUnder := base.Add(30*time.Minute - time.Second)
	if evs := e.Evaluate(justUnder, down); len(evs) != 0 {
		t.Fatalf("just-under cooldown should suppress, got %+v", evs)
	}

	// Exactly at the boundary: re-notify (gap >= cooldown).
	atBoundary := base.Add(30 * time.Minute)
	ev := singleEvent(t, e.Evaluate(atBoundary, down), Fire)
	if ev.At != atBoundary {
		t.Fatalf("re-fire timestamp = %v, want %v", ev.At, atBoundary)
	}

	// Immediately after the re-fire: clock restarted, suppressed again.
	if evs := e.Evaluate(atBoundary.Add(time.Minute), down); len(evs) != 0 {
		t.Fatalf("cooldown clock should restart after re-fire, got %+v", evs)
	}
}

func TestServiceDown_RecoversOnceAndReArms(t *testing.T) {
	e := New(Config{Host: "h"})

	singleEvent(t, e.Evaluate(base, down), Fire)

	// FIRING → OK: exactly one Recovery.
	rec := singleEvent(t, e.Evaluate(base.Add(5*time.Minute), up), Recovery)
	if rec.Detail != "wg-quick@wg0 active" {
		t.Fatalf("recovery detail = %q", rec.Detail)
	}

	// OK + good again: nothing (no duplicate recovery).
	if evs := e.Evaluate(base.Add(6*time.Minute), up); len(evs) != 0 {
		t.Fatalf("second good tick should emit nothing, got %+v", evs)
	}

	// Re-arm: a fresh OK→FIRING fires immediately (cooldown does not carry
	// over from the previous firing episode).
	singleEvent(t, e.Evaluate(base.Add(7*time.Minute), down), Fire)
}

func TestServiceDown_ReArmFromCurrentStateAfterRestart(t *testing.T) {
	// Simulate a restart: a fresh Evaluator with no prior state. A still-bad
	// input on the very first tick must fire exactly once.
	fresh := New(Config{Host: "h"})
	singleEvent(t, fresh.Evaluate(base, down), Fire)

	// And it must then suppress within cooldown, like any other firing.
	if evs := fresh.Evaluate(base.Add(time.Minute), down); len(evs) != 0 {
		t.Fatalf("post-restart fire should then suppress within cooldown, got %+v", evs)
	}
}

func TestEvaluate_NormalisesTimestampToUTC(t *testing.T) {
	e := New(Config{Host: "h"})
	loc := time.FixedZone("X", 5*3600)
	local := time.Date(2026, 6, 25, 15, 0, 0, 0, loc) // == 10:00:00Z
	ev := singleEvent(t, e.Evaluate(local, down), Fire)
	if ev.At.Location() != time.UTC {
		t.Fatalf("event At not UTC: %v", ev.At.Location())
	}
	if !ev.At.Equal(local) {
		t.Fatalf("UTC normalisation changed the instant: %v vs %v", ev.At, local)
	}
}

func TestFormatMessage(t *testing.T) {
	ev := Event{
		Condition: ConditionServiceDown,
		Key:       string(ConditionServiceDown),
		Kind:      Fire,
		Detail:    "wg-quick@wg0 not active",
		At:        base,
	}
	got := FormatMessage(ev, "wg-host-1")
	for _, want := range []string{"FIRING", "service-down", "wg-host-1", "wg-quick@wg0 not active", "2026-06-25T10:00:00Z"} {
		if !strings.Contains(got, want) {
			t.Fatalf("message %q missing %q", got, want)
		}
	}

	// Empty host falls back, never renders an empty "on ".
	if msg := FormatMessage(ev, ""); !strings.Contains(msg, "unknown-host") {
		t.Fatalf("empty host should fall back, got %q", msg)
	}

	// Recovery with empty detail omits the colon clause cleanly.
	rec := Event{Condition: ConditionServiceDown, Kind: Recovery, At: base}
	if msg := FormatMessage(rec, "h"); strings.Contains(msg, ": ") {
		t.Fatalf("empty-detail message should not contain a detail clause, got %q", msg)
	}
}
