package alerts

import (
	"testing"
	"time"
)

// TestActiveOnlyFiringKeys asserts Active() reports a key only while it is
// FIRING, drops it on recovery, and carries the latest detail string.
func TestActiveOnlyFiringKeys(t *testing.T) {
	e := New(Config{})

	// No conditions firing yet.
	if got := e.Active(); len(got) != 0 {
		t.Fatalf("fresh evaluator should have no active alerts, got %+v", got)
	}

	// Service goes down → service-down FIRING.
	e.Evaluate(base, down)
	got := e.Active()
	if len(got) != 1 {
		t.Fatalf("want one active alert, got %d: %+v", len(got), got)
	}
	if got[0].Condition != ConditionServiceDown || got[0].Key != "service-down" {
		t.Fatalf("unexpected active alert: %+v", got[0])
	}
	if got[0].Detail == "" {
		t.Fatalf("active alert should carry a detail string, got empty")
	}

	// Service recovers → key drops out of Active.
	e.Evaluate(base.Add(time.Minute), up)
	if got := e.Active(); len(got) != 0 {
		t.Fatalf("recovered key should drop from Active, got %+v", got)
	}
}

// TestActiveSinceStableAcrossCooldown asserts Since is the ORIGINAL fire time
// and is NOT bumped by a post-cooldown re-notify (a reminder Fire).
func TestActiveSinceStableAcrossCooldown(t *testing.T) {
	cooldown := 30 * time.Minute
	e := New(Config{Cooldown: cooldown})

	fireAt := base
	e.Evaluate(fireAt, down) // OK→FIRING at fireAt
	first := e.Active()
	if len(first) != 1 {
		t.Fatalf("want one active alert after fire, got %+v", first)
	}
	if !first[0].Since.Equal(fireAt) {
		t.Fatalf("Since = %v, want original fire time %v", first[0].Since, fireAt)
	}

	// A tick within cooldown (still bad) — no re-notify, Since unchanged.
	e.Evaluate(fireAt.Add(cooldown/2), down)
	if got := e.Active(); !got[0].Since.Equal(fireAt) {
		t.Fatalf("Since drifted within cooldown: got %v want %v", got[0].Since, fireAt)
	}

	// A tick AT/after the cooldown boundary — emits a reminder Fire, but the
	// machine stays FIRING, so Since must still be the original fire time.
	reminderAt := fireAt.Add(cooldown)
	evs := e.Evaluate(reminderAt, down)
	if len(evs) != 1 || evs[0].Kind != Fire {
		t.Fatalf("expected a reminder Fire at the cooldown boundary, got %+v", evs)
	}
	if got := e.Active(); !got[0].Since.Equal(fireAt) {
		t.Fatalf("Since bumped by cooldown re-notify: got %v want %v", got[0].Since, fireAt)
	}
}

// TestActiveSinceResetAfterRecovery asserts a recovered-then-refired key reports
// the NEW fire time (firingSince is cleared on recovery, set fresh on re-fire).
func TestActiveSinceResetAfterRecovery(t *testing.T) {
	e := New(Config{})

	e.Evaluate(base, down)                // fire #1
	e.Evaluate(base.Add(time.Minute), up) // recover
	refireAt := base.Add(2 * time.Minute) //
	e.Evaluate(refireAt, down)            // fire #2

	got := e.Active()
	if len(got) != 1 {
		t.Fatalf("want one active alert, got %+v", got)
	}
	if !got[0].Since.Equal(refireAt) {
		t.Fatalf("Since after re-fire = %v, want %v", got[0].Since, refireAt)
	}
}
