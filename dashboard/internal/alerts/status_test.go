package alerts

import (
	"sync"
	"testing"
	"time"
)

func TestStatusHolderSetEnabled(t *testing.T) {
	h := NewStatusHolder()
	if got := h.Snapshot(); got.Enabled {
		t.Fatalf("fresh holder should be disabled, got enabled")
	}
	h.SetEnabled(true)
	if got := h.Snapshot(); !got.Enabled {
		t.Fatalf("after SetEnabled(true), want enabled")
	}
	h.SetEnabled(false)
	if got := h.Snapshot(); got.Enabled {
		t.Fatalf("after SetEnabled(false), want disabled")
	}
}

func TestStatusHolderUpdateReplacesActive(t *testing.T) {
	h := NewStatusHolder()
	now := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	h.Update([]ActiveAlert{
		{Condition: ConditionServiceDown, Key: "service-down", Detail: "down", Since: now},
	}, nil)
	if got := h.Snapshot().Active; len(got) != 1 || got[0].Key != "service-down" {
		t.Fatalf("first update: want one active service-down, got %+v", got)
	}

	// A subsequent update REPLACES the active set wholesale (service recovered,
	// a disk alert now firing).
	h.Update([]ActiveAlert{
		{Condition: ConditionHighDisk, Key: "high-disk", Detail: "/ at 95%", Since: now},
	}, nil)
	got := h.Snapshot().Active
	if len(got) != 1 || got[0].Key != "high-disk" {
		t.Fatalf("second update should replace active set, got %+v", got)
	}
}

func TestStatusHolderSnapshotIsDeepCopy(t *testing.T) {
	h := NewStatusHolder()
	src := []ActiveAlert{{Condition: ConditionServiceDown, Key: "service-down"}}
	h.Update(src, nil)

	// Mutating the slice we passed in must not affect the holder.
	src[0].Key = "tampered"
	if got := h.Snapshot().Active; got[0].Key != "service-down" {
		t.Fatalf("holder retained caller slice; got %q", got[0].Key)
	}

	// Mutating one snapshot must not affect another.
	s1 := h.Snapshot()
	s1.Active[0].Key = "mutated"
	if got := h.Snapshot().Active; got[0].Key != "service-down" {
		t.Fatalf("snapshots share backing array; got %q", got[0].Key)
	}
}

func TestStatusHolderRecentNewestFirst(t *testing.T) {
	h := NewStatusHolder()
	t0 := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)
	h.Update(nil, []Event{
		{Condition: ConditionServiceDown, Key: "service-down", Kind: Fire, At: t0},
	})
	h.Update(nil, []Event{
		{Condition: ConditionServiceDown, Key: "service-down", Kind: Recovery, At: t0.Add(time.Minute)},
	})

	recent := h.Snapshot().Recent
	if len(recent) != 2 {
		t.Fatalf("want 2 recent entries, got %d", len(recent))
	}
	if recent[0].Kind != Recovery {
		t.Fatalf("recent should be newest-first; first entry kind = %v", recent[0].Kind)
	}
	if recent[1].Kind != Fire {
		t.Fatalf("second entry should be the older Fire, got %v", recent[1].Kind)
	}
}

func TestStatusHolderRecentEvictsAtCap(t *testing.T) {
	h := NewStatusHolder()
	base := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	// Push well past the cap, one event per update.
	total := recentCap + 20
	for i := 0; i < total; i++ {
		h.Update(nil, []Event{
			{Condition: ConditionServiceDown, Key: "service-down", Kind: Fire, At: base.Add(time.Duration(i) * time.Second)},
		})
	}

	recent := h.Snapshot().Recent
	if len(recent) != recentCap {
		t.Fatalf("ring should cap at %d, got %d", recentCap, len(recent))
	}
	// Newest-first: the very last pushed event must be at index 0.
	wantNewest := base.Add(time.Duration(total-1) * time.Second)
	if !recent[0].At.Equal(wantNewest) {
		t.Fatalf("newest entry At = %v, want %v", recent[0].At, wantNewest)
	}
	// The oldest surviving entry is total-recentCap (older ones evicted).
	wantOldest := base.Add(time.Duration(total-recentCap) * time.Second)
	if !recent[len(recent)-1].At.Equal(wantOldest) {
		t.Fatalf("oldest surviving entry At = %v, want %v", recent[len(recent)-1].At, wantOldest)
	}
}

// TestStatusHolderRace hammers Update (a simulated poller) against many
// concurrent Snapshot readers (simulated handlers). Run under -race this is the
// regression guard for the holder being the ONLY safe channel between the
// poller goroutine and the HTTP goroutines.
func TestStatusHolderRace(t *testing.T) {
	h := NewStatusHolder()
	h.SetEnabled(true)
	base := time.Date(2026, 6, 25, 10, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// One writer goroutine = the poller.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			at := base.Add(time.Duration(i) * time.Second)
			h.Update(
				[]ActiveAlert{{Condition: ConditionServiceDown, Key: "service-down", Detail: "x", Since: at}},
				[]Event{{Condition: ConditionServiceDown, Key: "service-down", Kind: Fire, At: at}},
			)
		}
	}()

	// Many reader goroutines = HTTP handlers.
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				snap := h.Snapshot()
				// Touch the copied data so the race detector sees the reads.
				_ = snap.Enabled
				for _, a := range snap.Active {
					_ = a.Key
				}
				for _, e := range snap.Recent {
					_ = e.Key
				}
			}
		}()
	}

	// Let readers finish, then stop the writer.
	time.Sleep(20 * time.Millisecond)
	close(stop)
	wg.Wait()
}
