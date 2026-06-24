// Package history derives per-peer connection history from the handshake
// samples the poller persists to SQLite (the handshake_events table). It is
// pure: every function operates on values the caller passes in — an ordered
// slice of handshake timestamps plus an injected "now" and the relevant
// thresholds — performs no I/O, and is therefore trivially table-testable
// with synthetic sample sets.
//
// WireGuard has no explicit connect/disconnect signal: a peer simply completes
// a fresh handshake roughly every two minutes while it has traffic to move. We
// reconstruct "sessions" from that cadence — a run of handshakes whose
// consecutive gaps stay within SessionGapThreshold is one session; a gap larger
// than the threshold closes the previous session and opens a new one. This is
// the model spec 006 §2.1 describes; no new storage is involved, the derivation
// runs at query time over already-retained samples.
package history

import (
	"fmt"
	"time"
)

// SessionGapThreshold is the inter-handshake gap above which a new session is
// considered to have started. WireGuard re-handshakes about every two minutes
// while a peer is active, so a gap beyond ten minutes means the peer was almost
// certainly disconnected in between. The boundary is exclusive: a gap of
// exactly SessionGapThreshold keeps the same session; only a strictly larger
// gap opens a new one (functional spec §2.1).
const SessionGapThreshold = 10 * time.Minute

// Session is one inferred connection span: the timestamps of the first and last
// handshake in a run. A single-handshake session has Start == End and therefore
// a zero duration — a lone handshake is a point in time, not an interval, so it
// contributes nothing to connected time.
type Session struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// Duration is End-Start. Zero for a single-handshake session.
func (s Session) Duration() time.Duration { return s.End.Sub(s.Start) }

// Summary is the derived connection history for one peer over a range.
type Summary struct {
	// Sessions are the inferred connection spans in chronological order. Never
	// nil — an empty history yields a non-nil, zero-length slice so JSON
	// callers get [] rather than null.
	Sessions []Session
	// SessionCount == len(Sessions); carried explicitly so JSON consumers don't
	// have to count.
	SessionCount int
	// ConnectedTime is the sum of every session's duration within the range.
	ConnectedTime time.Duration
	// Online is true when the most recent handshake is within onlineThreshold
	// of now — the same rule the live client list uses
	// (internal/server/clientrows.go).
	Online bool
	// LastSeen is the most recent handshake time, or the zero value if the peer
	// has never handshaked in the supplied samples.
	LastSeen time.Time
	// LastSeenText is a human-readable rendering of LastSeen relative to now
	// ("2 days ago", "5 minutes ago", "just now"), or "never" when LastSeen is
	// zero.
	LastSeenText string
}

// Derive reconstructs a peer's connection Summary from handshake timestamps.
//
// handshakes must be ordered oldest-first (the db.QueryHandshakeEventsByKey
// helper returns them ASC by ts); Derive does not sort, to stay allocation-free
// on the hot 10s-tick path. now and onlineThreshold are injected so the result
// is deterministic under test and so the online rule stays a single source of
// truth — callers pass the same onlineThreshold the live client list uses.
// gapThreshold is normally SessionGapThreshold but is a parameter so the
// boundary is directly testable.
func Derive(handshakes []time.Time, now time.Time, gapThreshold, onlineThreshold time.Duration) Summary {
	sum := Summary{
		// Non-nil empty slice so the JSON encoder emits [] not null for a peer
		// with no handshakes in the range.
		Sessions:     make([]Session, 0),
		LastSeenText: "never",
	}
	if len(handshakes) == 0 {
		return sum
	}

	// Walk the ordered timestamps, splitting into sessions on any gap that
	// strictly exceeds gapThreshold. start tracks the current session's first
	// handshake; prev the previous handshake we compared against.
	start := handshakes[0]
	prev := handshakes[0]
	for _, ts := range handshakes[1:] {
		if ts.Sub(prev) > gapThreshold {
			sum.Sessions = append(sum.Sessions, Session{Start: start, End: prev})
			start = ts
		}
		prev = ts
	}
	sum.Sessions = append(sum.Sessions, Session{Start: start, End: prev})

	sum.SessionCount = len(sum.Sessions)
	for _, s := range sum.Sessions {
		sum.ConnectedTime += s.Duration()
	}

	last := handshakes[len(handshakes)-1]
	sum.LastSeen = last
	sum.Online = now.Sub(last) <= onlineThreshold
	sum.LastSeenText = humanizeSince(now, last)
	return sum
}

// humanizeSince renders the gap between now and t as a coarse human phrase. It
// uses full-word units ("2 days ago") to match the spec's wording, distinct
// from the server's abbreviated humanAgo ("2d ago"), and takes now explicitly
// so it's deterministic under test. A future t (clock skew) collapses to
// "just now".
func humanizeSince(now, t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := now.Sub(t)
	if d < time.Minute {
		return "just now"
	}
	switch {
	case d < time.Hour:
		return plural(int(d.Minutes()), "minute")
	case d < 24*time.Hour:
		return plural(int(d.Hours()), "hour")
	default:
		return plural(int(d.Hours())/24, "day")
	}
}

// plural renders "1 minute ago" / "3 minutes ago" — the singular form drops the
// trailing "s".
func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s ago", unit)
	}
	return fmt.Sprintf("%d %ss ago", n, unit)
}
