package alerts

import (
	"sync"
	"time"
)

// StatusHolder is the ONLY safe channel between the poller goroutine (which
// drives the Evaluator) and the HTTP goroutines (which render the in-UI
// active-alerts view). The Evaluator itself is NOT concurrency-safe — it is
// touched exclusively from the poller's single sample loop — so the server must
// never call Evaluate/Active or read the evaluator's maps. Instead the poller
// writes a snapshot into this holder once per tick (Update) and the server
// reads a deep copy (Snapshot) from request goroutines. Every field access is
// guarded by mu; Snapshot returns freshly-allocated slices so the caller can
// read them without holding the lock.
//
// State here is purely in-memory (spec 007 §3 forbids an alert datastore): the
// Active slice is replaced wholesale each tick, and Recent is a bounded ring of
// the most recent fire/recovery transitions for the Events tab. A process
// restart starts both empty — consistent with the evaluator re-arming from OK.
type StatusHolder struct {
	mu      sync.Mutex
	enabled bool
	active  []ActiveAlert
	// recent is a bounded ring of the most recent transitions, oldest-first.
	// Capped at recentCap so a long-running process can't grow it without bound;
	// Snapshot reverses it so the server renders newest-first.
	recent []LogEntry
}

// recentCap bounds the in-memory transition ring. ~50 keeps the Events tab
// useful without unbounded growth; older entries are evicted FIFO.
const recentCap = 50

// ActiveAlert is one currently-FIRING condition as the server renders it. Since
// is the original OK→FIRING fire time (STABLE across cooldown re-notifies, not
// bumped), so the UI can show a truthful "firing since" age. Detail is the most
// recent measured-value string for the condition.
type ActiveAlert struct {
	Condition Condition `json:"condition"`
	Key       string    `json:"key"`
	Detail    string    `json:"detail"`
	Since     time.Time `json:"since"`
}

// LogEntry is one fire/recovery transition recorded in the ring for the Events
// tab. Kind is Fire or Recovery; At is the tick timestamp the transition was
// observed.
type LogEntry struct {
	Condition Condition `json:"condition"`
	Key       string    `json:"key"`
	Kind      Kind      `json:"kind"`
	Detail    string    `json:"detail"`
	At        time.Time `json:"at"`
}

// NewStatusHolder returns an empty holder. enabled defaults to false until
// main.go calls SetEnabled once at wiring time.
func NewStatusHolder() *StatusHolder {
	return &StatusHolder{}
}

// SetEnabled records whether outbound webhook alerting is configured. Called
// ONCE by main.go (a webhook URL is present or not for the life of the process)
// but guarded by the mutex anyway so it is safe relative to concurrent reads.
func (h *StatusHolder) SetEnabled(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = enabled
}

// Update is the poller-side write, called once per tick from the sample-loop
// goroutine. It replaces the active set wholesale (the evaluator's current
// firing keys) and appends each transition event to the bounded ring, evicting
// the oldest entries past recentCap. The passed slices are copied — the caller
// may retain or mutate them after the call.
func (h *StatusHolder) Update(active []ActiveAlert, events []Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.active = make([]ActiveAlert, len(active))
	copy(h.active, active)

	for _, ev := range events {
		h.recent = append(h.recent, LogEntry{
			Condition: ev.Condition,
			Key:       ev.Key,
			Kind:      ev.Kind,
			Detail:    ev.Detail,
			At:        ev.At,
		})
	}
	// Evict oldest beyond the cap. Re-slice onto a fresh backing array so the
	// dropped entries are not retained and the ring can't creep past recentCap.
	if len(h.recent) > recentCap {
		trimmed := make([]LogEntry, recentCap)
		copy(trimmed, h.recent[len(h.recent)-recentCap:])
		h.recent = trimmed
	}
}

// Status is the deep-copied view the server renders. All slices are freshly
// allocated by Snapshot, so the caller reads them without holding the lock.
// Recent is newest-first (the ring stores oldest-first).
type Status struct {
	Enabled bool          `json:"enabled"`
	Active  []ActiveAlert `json:"active"`
	Recent  []LogEntry    `json:"recent"`
}

// Snapshot returns a deep copy of the holder's current state, safe to read
// without the lock. Active is copied as-is (already condition order from the
// evaluator); Recent is reversed to newest-first for the Events tab. Both
// slices are non-nil even when empty so the JSON encoder emits `[]`, never
// `null`, and the templates iterate cleanly.
func (h *StatusHolder) Snapshot() Status {
	h.mu.Lock()
	defer h.mu.Unlock()

	active := make([]ActiveAlert, len(h.active))
	copy(active, h.active)

	recent := make([]LogEntry, len(h.recent))
	for i, e := range h.recent {
		recent[len(h.recent)-1-i] = e
	}

	return Status{
		Enabled: h.enabled,
		Active:  active,
		Recent:  recent,
	}
}
