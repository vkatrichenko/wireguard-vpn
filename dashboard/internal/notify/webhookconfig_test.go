package notify

import (
	"strings"
	"sync"
	"testing"
)

const (
	holderSeed  = "https://hooks.example.com/services/SEED/TOKEN/aaa"
	holderOther = "https://other.example.net/services/OVR/TOKEN/bbb"
)

func TestWebhookConfig_CurrentSetRevert(t *testing.T) {
	c := NewWebhookConfig(holderSeed)

	if got := c.Current(); got != holderSeed {
		t.Fatalf("Current() = %q, want seed %q", got, holderSeed)
	}

	c.Set(holderOther)
	if got := c.Current(); got != holderOther {
		t.Fatalf("after Set, Current() = %q, want override %q", got, holderOther)
	}

	c.Revert()
	if got := c.Current(); got != holderSeed {
		t.Fatalf("after Revert, Current() = %q, want seed %q", got, holderSeed)
	}
}

func TestWebhookConfig_Enabled(t *testing.T) {
	c := NewWebhookConfig(holderSeed)
	if !c.Enabled() {
		t.Error("seeded holder should be Enabled")
	}

	c.Set("")
	if c.Enabled() {
		t.Error("empty override should disable")
	}

	c.Revert()
	if !c.Enabled() {
		t.Error("after Revert to non-empty seed, should be Enabled")
	}

	empty := NewWebhookConfig("")
	if empty.Enabled() {
		t.Error("empty-seed holder should be disabled")
	}
	empty.Set(holderOther)
	if !empty.Enabled() {
		t.Error("override on empty seed should enable")
	}
}

func TestWebhookConfig_Status(t *testing.T) {
	t.Run("seed only", func(t *testing.T) {
		st := NewWebhookConfig(holderSeed).Status()
		if !st.Enabled {
			t.Error("want Enabled")
		}
		if st.OverrideActive {
			t.Error("want OverrideActive=false on seed-only")
		}
		if st.MaskedURL != "https://hooks.example.com" {
			t.Errorf("MaskedURL = %q, want scheme+host", st.MaskedURL)
		}
	})

	t.Run("override active", func(t *testing.T) {
		c := NewWebhookConfig(holderSeed)
		c.Set(holderOther)
		st := c.Status()
		if !st.Enabled || !st.OverrideActive {
			t.Errorf("want Enabled && OverrideActive, got %+v", st)
		}
		if st.MaskedURL != "https://other.example.net" {
			t.Errorf("MaskedURL = %q, want override scheme+host", st.MaskedURL)
		}
	})

	t.Run("empty seed", func(t *testing.T) {
		st := NewWebhookConfig("").Status()
		if st.Enabled || st.OverrideActive || st.MaskedURL != "" {
			t.Errorf("empty seed: want zero Status, got %+v", st)
		}
	})

	t.Run("empty override disables but override active", func(t *testing.T) {
		c := NewWebhookConfig(holderSeed)
		c.Set("")
		st := c.Status()
		if st.Enabled {
			t.Error("empty override should not be Enabled")
		}
		if !st.OverrideActive {
			t.Error("empty override is still an active override")
		}
		if st.MaskedURL != "" {
			t.Errorf("MaskedURL = %q, want empty", st.MaskedURL)
		}
	})
}

// MaskedURL must never leak the secret path/token.
func TestWebhookConfig_StatusMaskingNeverLeaksToken(t *testing.T) {
	c := NewWebhookConfig(holderSeed)
	st := c.Status()
	for _, leak := range []string{"SEED", "TOKEN", "aaa", "/services/"} {
		if strings.Contains(st.MaskedURL, leak) {
			t.Errorf("MaskedURL %q leaked %q", st.MaskedURL, leak)
		}
	}
}

// Concurrent readers vs writers — run with -race.
func TestWebhookConfig_ConcurrentAccess(t *testing.T) {
	c := NewWebhookConfig(holderSeed)
	var wg sync.WaitGroup
	const goroutines = 8
	const iters = 2000

	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = c.Current()
				_ = c.Enabled()
				_ = c.Status()
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				if j%2 == 0 {
					c.Set(holderOther)
				} else {
					c.Revert()
				}
			}
		}()
	}
	wg.Wait()
}
