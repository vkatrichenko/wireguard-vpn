package alerts

import (
	"strings"
	"testing"
	"time"
)

// peerInput builds an Input with the service held active (so only the per-peer
// conditions can contribute events) carrying the given peers.
func peerInput(peers ...PeerSample) Input {
	return Input{ServiceActive: true, Peers: peers}
}

// hsAgo returns a PeerSample for name whose last handshake is age before now.
func hsAgo(name string, now time.Time, age time.Duration) PeerSample {
	return PeerSample{Name: name, LastHandshake: now.Add(-age)}
}

func TestPeerDown_NeverOnlineNeverFires(t *testing.T) {
	e := New(Config{Host: "h"})

	// A peer that has NEVER been within the online threshold, even though its
	// (stale) handshake is far older than the stale threshold, must never fire:
	// the seen-online gate has never latched.
	for i := 0; i < 5; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		// Last handshake always 1h ago — never within the 3m online window.
		in := peerInput(hsAgo("ghost", now, time.Hour))
		if evs := e.Evaluate(now, in); len(evs) != 0 {
			t.Fatalf("never-online peer must not fire, tick %d got %+v", i, evs)
		}
	}

	// A peer that never handshaked at all (zero time) also never fires.
	e2 := New(Config{Host: "h"})
	if evs := e2.Evaluate(base, peerInput(PeerSample{Name: "newbie"})); len(evs) != 0 {
		t.Fatalf("zero-handshake peer must not fire, got %+v", evs)
	}
}

func TestPeerDown_SeenThenIdleFiresOnce(t *testing.T) {
	e := New(Config{Host: "h"})

	// Tick 1: online (fresh handshake) — latches seenOnline, no event.
	if evs := e.Evaluate(base, peerInput(hsAgo("alice", base, time.Minute))); len(evs) != 0 {
		t.Fatalf("online peer should not fire, got %+v", evs)
	}
	if !e.seenOnline["alice"] {
		t.Fatalf("seenOnline should latch true after an online tick")
	}

	// The handshake then freezes; later ticks show it ageing. Just under 10m: OK.
	now := base.Add(20 * time.Minute)
	justUnder := PeerSample{Name: "alice", LastHandshake: now.Add(-(10*time.Minute - time.Second))}
	if evs := e.Evaluate(now, peerInput(justUnder)); len(evs) != 0 {
		t.Fatalf("just-under stale threshold should suppress, got %+v", evs)
	}

	// Over 10m: fires exactly once.
	now2 := now.Add(time.Minute)
	overStale := PeerSample{Name: "alice", LastHandshake: now2.Add(-(10*time.Minute + time.Second))}
	ev := onlyEvent(t, e.Evaluate(now2, peerInput(overStale)), ConditionPeerDown, Fire)
	if ev.Key != "peer-down:alice" {
		t.Fatalf("key = %q, want peer-down:alice", ev.Key)
	}
	if !strings.Contains(ev.Detail, "alice") || !strings.Contains(ev.Detail, "handshake") {
		t.Fatalf("detail should name peer + handshake, got %q", ev.Detail)
	}

	// Still stale next tick: suppressed within cooldown.
	now3 := now2.Add(time.Minute)
	stillStale := PeerSample{Name: "alice", LastHandshake: now3.Add(-(11 * time.Minute))}
	if evs := e.Evaluate(now3, peerInput(stillStale)); len(evs) != 0 {
		t.Fatalf("still-stale within cooldown should suppress, got %+v", evs)
	}
}

func TestPeerDown_RecoversOnHandshake(t *testing.T) {
	e := New(Config{Host: "h"})

	e.Evaluate(base, peerInput(hsAgo("alice", base, time.Minute))) // seen online
	now := base.Add(30 * time.Minute)
	onlyEvent(t, e.Evaluate(now, peerInput(hsAgo("alice", now, 11*time.Minute))), ConditionPeerDown, Fire)

	// Handshakes again (back within the online window): one Recovery.
	now2 := now.Add(time.Minute)
	onlyEvent(t, e.Evaluate(now2, peerInput(hsAgo("alice", now2, time.Minute))), ConditionPeerDown, Recovery)

	// The seen-online flag persists, and it's online, so nothing further.
	now3 := now2.Add(time.Minute)
	if evs := e.Evaluate(now3, peerInput(hsAgo("alice", now3, time.Minute))); len(evs) != 0 {
		t.Fatalf("recovered+online should emit nothing, got %+v", evs)
	}
	if !e.seenOnline["alice"] {
		t.Fatalf("seenOnline must persist across the fire/recover cycle")
	}
}

func capPeer(name string, rx, tx int64) PeerSample {
	// A fresh handshake so peer-down never interferes with transfer-cap tests.
	return PeerSample{Name: name, LastHandshake: time.Time{}, RxBytes: rx, TxBytes: tx}
}

func TestTransferCap_FiresOnceWhenRxCrosses(t *testing.T) {
	cap := int64(50 << 30)
	e := New(Config{Host: "h", TransferCapBytes: cap})

	// First observation: baseline taken here (non-zero), so the cap is measured
	// from this point, NOT from zero.
	if evs := e.Evaluate(base, peerInput(capPeer("bob", 10<<30, 0))); len(evs) != 0 {
		t.Fatalf("first observation should set baseline, not fire, got %+v", evs)
	}
	if got := e.transferBaseline["bob"].rx; got != 10<<30 {
		t.Fatalf("baseline rx = %d, want %d", got, int64(10<<30))
	}

	// rx grows but delta is still under cap (10GiB + 49GiB = 59GiB total, delta 49GiB).
	if evs := e.Evaluate(base.Add(time.Minute), peerInput(capPeer("bob", 59<<30, 0))); len(evs) != 0 {
		t.Fatalf("delta under cap should not fire, got %+v", evs)
	}

	// Delta reaches the cap (>= inclusive): fires once.
	ev := onlyEvent(t, e.Evaluate(base.Add(2*time.Minute), peerInput(capPeer("bob", 60<<30, 0))), ConditionTransferCap, Fire)
	if ev.Key != "transfer-cap:bob" {
		t.Fatalf("key = %q, want transfer-cap:bob", ev.Key)
	}
	if !strings.Contains(ev.Detail, "bob") || !strings.Contains(ev.Detail, "downloaded") {
		t.Fatalf("rx-direction detail should say downloaded, got %q", ev.Detail)
	}

	// Still over, within cooldown: suppressed.
	if evs := e.Evaluate(base.Add(3*time.Minute), peerInput(capPeer("bob", 70<<30, 0))); len(evs) != 0 {
		t.Fatalf("still over within cooldown should suppress, got %+v", evs)
	}
}

func TestTransferCap_FiresOnTxDirection(t *testing.T) {
	cap := int64(50 << 30)
	e := New(Config{Host: "h", TransferCapBytes: cap})

	e.Evaluate(base, peerInput(capPeer("bob", 0, 0))) // baseline at 0
	ev := onlyEvent(t, e.Evaluate(base.Add(time.Minute), peerInput(capPeer("bob", 0, 51<<30))), ConditionTransferCap, Fire)
	if !strings.Contains(ev.Detail, "uploaded") {
		t.Fatalf("tx-direction detail should say uploaded, got %q", ev.Detail)
	}
}

func TestTransferCap_CounterResetReArms(t *testing.T) {
	cap := int64(50 << 30)
	e := New(Config{Host: "h", TransferCapBytes: cap})

	// Baseline non-zero so a later reset can drop strictly below it.
	e.Evaluate(base, peerInput(capPeer("carol", 100<<30, 0)))
	onlyEvent(t, e.Evaluate(base.Add(time.Minute), peerInput(capPeer("carol", 160<<30, 0))), ConditionTransferCap, Fire)

	// The wg counter resets to a tiny value (< baseline 100GiB) — interface/peer
	// re-add. The baseline re-bases to the current value, the running total
	// restarts from zero, and the state machine recovers + re-arms.
	onlyEvent(t, e.Evaluate(base.Add(2*time.Minute), peerInput(capPeer("carol", 1<<30, 0))), ConditionTransferCap, Recovery)
	if got := e.transferBaseline["carol"].rx; got != 1<<30 {
		t.Fatalf("baseline should reset to current %d, got %d", int64(1<<30), got)
	}

	// Growth from the new baseline can fire AGAIN once it crosses the cap.
	onlyEvent(t, e.Evaluate(base.Add(3*time.Minute), peerInput(capPeer("carol", (1<<30)+(50<<30), 0))), ConditionTransferCap, Fire)
}

// TestPerClientIsolation proves the per-name Keys give each (peer, condition)
// pair an independent state machine: alice firing peer-down leaves
// transfer-cap:alice, peer-down:bob and service-down untouched.
func TestPerClientIsolation(t *testing.T) {
	e := New(Config{Host: "h"})

	// Both seen online first.
	e.Evaluate(base, peerInput(
		hsAgo("alice", base, time.Minute),
		hsAgo("bob", base, time.Minute),
	))

	now := base.Add(30 * time.Minute)
	// alice goes stale (fires peer-down); bob stays fresh; service stays up.
	in := Input{
		ServiceActive: true,
		Peers: []PeerSample{
			hsAgo("alice", now, 11*time.Minute), // stale → fire
			hsAgo("bob", now, time.Minute),      // fresh → ok
		},
	}
	evs := e.Evaluate(now, in)
	ev := onlyEvent(t, evs, ConditionPeerDown, Fire)
	if ev.Key != "peer-down:alice" {
		t.Fatalf("only alice should fire, got key %q", ev.Key)
	}

	// Independent machines untouched (all OK / never-fired).
	for _, key := range []string{"transfer-cap:alice", "peer-down:bob", string(ConditionServiceDown)} {
		if sm := e.states[key]; sm != nil && sm.firing {
			t.Fatalf("key %q should not be firing — alice's peer-down leaked state", key)
		}
	}
}

func TestPeerDefaultsApplied(t *testing.T) {
	e := New(Config{Host: "h"})
	if e.peerOnlineThreshold != DefaultPeerOnlineThreshold {
		t.Fatalf("peer online default = %v, want %v", e.peerOnlineThreshold, DefaultPeerOnlineThreshold)
	}
	if e.peerStaleThreshold != DefaultPeerStaleThreshold {
		t.Fatalf("peer stale default = %v, want %v", e.peerStaleThreshold, DefaultPeerStaleThreshold)
	}
	if e.transferCapBytes != DefaultTransferCapBytes {
		t.Fatalf("transfer cap default = %v, want %v", e.transferCapBytes, DefaultTransferCapBytes)
	}
}
