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

// TestStalePeerNeverFires is the peer-down-removal regression guard (spec 012
// §2.3): a client that was online and then goes stale/disconnected — an old
// handshake or a zero (never-handshaked) one — must produce NO alert and leave
// NO active-alert entry. Turning a VPN client off is normal behaviour, not an
// incident, so the evaluator stays silent across every such tick.
func TestStalePeerNeverFires(t *testing.T) {
	e := New(Config{Host: "h"})

	// Tick 1: alice online with a fresh handshake.
	if evs := e.Evaluate(base, peerInput(hsAgo("alice", base, time.Minute))); len(evs) != 0 {
		t.Fatalf("online peer should not fire, got %+v", evs)
	}

	// Ticks 2..N: the handshake freezes and ages well past any old stale window;
	// a previously-online peer going idle must never fire.
	for i := 1; i <= 5; i++ {
		now := base.Add(time.Duration(i) * 30 * time.Minute)
		stale := PeerSample{Name: "alice", LastHandshake: base.Add(time.Minute)}
		if evs := e.Evaluate(now, peerInput(stale)); len(evs) != 0 {
			t.Fatalf("stale peer must not fire (tick %d), got %+v", i, evs)
		}
	}

	// A peer that never handshaked at all (zero time) also never fires.
	if evs := e.Evaluate(base.Add(3*time.Hour), peerInput(PeerSample{Name: "newbie"})); len(evs) != 0 {
		t.Fatalf("zero-handshake peer must not fire, got %+v", evs)
	}

	// And nothing is left FIRING in the active-alerts view.
	if act := e.Active(); len(act) != 0 {
		t.Fatalf("no peer condition should be active, got %+v", act)
	}
}

func capPeer(name string, rx, tx int64) PeerSample {
	// No condition keys off the handshake time; the transfer-cap tests care only
	// about the byte counters.
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

// TestPerClientIsolation proves the per-name Keys give each peer's transfer-cap
// its own independent state machine: bob crossing the cap leaves
// transfer-cap:carol and service-down untouched.
func TestPerClientIsolation(t *testing.T) {
	cap := int64(50 << 30)
	e := New(Config{Host: "h", TransferCapBytes: cap})

	// Both baselined at zero.
	e.Evaluate(base, peerInput(capPeer("bob", 0, 0), capPeer("carol", 0, 0)))

	// bob crosses the cap; carol stays well under; service stays up.
	in := Input{
		ServiceActive: true,
		Peers: []PeerSample{
			capPeer("bob", 60<<30, 0),  // over → fire
			capPeer("carol", 1<<30, 0), // under → ok
		},
	}
	ev := onlyEvent(t, e.Evaluate(base.Add(time.Minute), in), ConditionTransferCap, Fire)
	if ev.Key != "transfer-cap:bob" {
		t.Fatalf("only bob should fire, got key %q", ev.Key)
	}

	// Independent machines untouched (all OK / never-fired).
	for _, key := range []string{"transfer-cap:carol", string(ConditionServiceDown)} {
		if sm := e.states[key]; sm != nil && sm.firing {
			t.Fatalf("key %q should not be firing — bob's transfer-cap leaked state", key)
		}
	}
}

func TestPeerDefaultsApplied(t *testing.T) {
	e := New(Config{Host: "h"})
	if e.transferCapBytes != DefaultTransferCapBytes {
		t.Fatalf("transfer cap default = %v, want %v", e.transferCapBytes, DefaultTransferCapBytes)
	}
}
