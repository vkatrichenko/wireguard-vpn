package history

import (
	"testing"
	"time"
)

// anchor is a fixed "now" so every case is deterministic regardless of when the
// suite runs. Handshake timestamps below are expressed as offsets before it.
var anchor = time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)

// ago returns anchor - d, the natural way to place a handshake "d ago".
func ago(d time.Duration) time.Time { return anchor.Add(-d) }

func TestDerive_Sessions(t *testing.T) {
	const online = 3 * time.Minute

	tests := []struct {
		name string
		// handshakes are oldest-first, as the db query returns them.
		handshakes    []time.Time
		wantSessions  int
		wantConnected time.Duration
		wantOnline    bool
		wantLastSeen  string
	}{
		{
			name:          "none",
			handshakes:    nil,
			wantSessions:  0,
			wantConnected: 0,
			wantOnline:    false,
			wantLastSeen:  "never",
		},
		{
			name:          "single handshake is a zero-length session",
			handshakes:    []time.Time{ago(2 * time.Hour)},
			wantSessions:  1,
			wantConnected: 0,
			wantOnline:    false,
			wantLastSeen:  "2 hours ago",
		},
		{
			// Five handshakes 2m apart spanning the last ~8 minutes: one
			// continuous session, last one 0s ago → online.
			name: "continuous run is one session",
			handshakes: []time.Time{
				ago(8 * time.Minute),
				ago(6 * time.Minute),
				ago(4 * time.Minute),
				ago(2 * time.Minute),
				ago(0),
			},
			wantSessions:  1,
			wantConnected: 8 * time.Minute,
			wantOnline:    true,
			wantLastSeen:  "just now",
		},
		{
			// Two clusters 30m apart → two sessions. Each cluster spans 4m.
			name: "gap splits into two sessions",
			handshakes: []time.Time{
				ago(50 * time.Minute),
				ago(48 * time.Minute),
				ago(46 * time.Minute), // cluster A: 50→46m ago, span 4m
				ago(10 * time.Minute),
				ago(8 * time.Minute),
				ago(6 * time.Minute), // cluster B: 10→6m ago, span 4m
			},
			wantSessions:  2,
			wantConnected: 8 * time.Minute,
			wantOnline:    false, // last handshake 6m ago > 3m
			wantLastSeen:  "6 minutes ago",
		},
		{
			// Boundary: an exactly-10-minute gap does NOT open a new session
			// (the threshold is exclusive — only a gap > 10m splits).
			name: "exactly 10m gap stays one session",
			handshakes: []time.Time{
				ago(20 * time.Minute),
				ago(10 * time.Minute), // gap to previous is exactly 10m
			},
			wantSessions:  1,
			wantConnected: 10 * time.Minute,
			wantOnline:    false,
			wantLastSeen:  "10 minutes ago",
		},
		{
			// Boundary: a gap just over 10 minutes (10m1s) opens a new session.
			name: "just over 10m gap opens a new session",
			handshakes: []time.Time{
				ago(20*time.Minute + 1*time.Second),
				ago(10 * time.Minute), // gap is 10m1s → split
			},
			wantSessions:  2,
			wantConnected: 0, // both sessions are single handshakes
			wantOnline:    false,
			wantLastSeen:  "10 minutes ago",
		},
		{
			name:          "recent single handshake is online",
			handshakes:    []time.Time{ago(1 * time.Minute)},
			wantSessions:  1,
			wantConnected: 0,
			wantOnline:    true,
			wantLastSeen:  "1 minute ago",
		},
		{
			name:          "handshake older than threshold is offline",
			handshakes:    []time.Time{ago(5 * time.Minute)},
			wantSessions:  1,
			wantConnected: 0,
			wantOnline:    false,
			wantLastSeen:  "5 minutes ago",
		},
		{
			name:          "days ago renders in days",
			handshakes:    []time.Time{ago(48 * time.Hour)},
			wantSessions:  1,
			wantConnected: 0,
			wantOnline:    false,
			wantLastSeen:  "2 days ago",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Derive(tc.handshakes, anchor, SessionGapThreshold, online)

			if got.Sessions == nil {
				t.Errorf("Sessions is nil, want non-nil slice (JSON [] not null)")
			}
			if got.SessionCount != tc.wantSessions {
				t.Errorf("SessionCount = %d, want %d", got.SessionCount, tc.wantSessions)
			}
			if got.SessionCount != len(got.Sessions) {
				t.Errorf("SessionCount %d != len(Sessions) %d", got.SessionCount, len(got.Sessions))
			}
			if got.ConnectedTime != tc.wantConnected {
				t.Errorf("ConnectedTime = %s, want %s", got.ConnectedTime, tc.wantConnected)
			}
			if got.Online != tc.wantOnline {
				t.Errorf("Online = %v, want %v", got.Online, tc.wantOnline)
			}
			if got.LastSeenText != tc.wantLastSeen {
				t.Errorf("LastSeenText = %q, want %q", got.LastSeenText, tc.wantLastSeen)
			}
		})
	}
}

// TestDerive_LastSeenAndSpans pins the session-span boundaries and LastSeen
// value for the two-cluster case — the table above asserts counts/text, this
// asserts the actual Start/End timestamps so a future off-by-one in the split
// logic is caught.
func TestDerive_SessionSpans(t *testing.T) {
	handshakes := []time.Time{
		ago(50 * time.Minute),
		ago(46 * time.Minute), // cluster A
		ago(10 * time.Minute),
		ago(6 * time.Minute), // cluster B
	}
	got := Derive(handshakes, anchor, SessionGapThreshold, 3*time.Minute)

	if len(got.Sessions) != 2 {
		t.Fatalf("len(Sessions) = %d, want 2", len(got.Sessions))
	}
	if !got.Sessions[0].Start.Equal(ago(50*time.Minute)) || !got.Sessions[0].End.Equal(ago(46*time.Minute)) {
		t.Errorf("session[0] = %v..%v, want %v..%v",
			got.Sessions[0].Start, got.Sessions[0].End, ago(50*time.Minute), ago(46*time.Minute))
	}
	if !got.Sessions[1].Start.Equal(ago(10*time.Minute)) || !got.Sessions[1].End.Equal(ago(6*time.Minute)) {
		t.Errorf("session[1] = %v..%v, want %v..%v",
			got.Sessions[1].Start, got.Sessions[1].End, ago(10*time.Minute), ago(6*time.Minute))
	}
	if !got.LastSeen.Equal(ago(6 * time.Minute)) {
		t.Errorf("LastSeen = %v, want %v", got.LastSeen, ago(6*time.Minute))
	}
}
