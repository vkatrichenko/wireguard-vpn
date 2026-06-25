package notify

import "sync"

// WebhookConfig is the runtime-mutable holder for the alert webhook URL (spec
// 008). It exists so an operator can re-point or disable delivery at runtime
// (via the Slice-2 HTTP endpoint) without a restart, while the notifier resolves
// its target afresh on every send. There are two layers:
//
//   - seed — the immutable boot value (DASHBOARD_WEBHOOK_URL at startup). It is
//     never mutated; Revert restores it.
//   - override — an optional in-memory replacement set at runtime. nil means
//     "no override, use the seed". The override is deliberately NOT persisted:
//     a restart returns the holder to the seed, which is the whole point of the
//     env-var-as-source-of-truth model.
//
// All access is guarded by a sync.RWMutex so concurrent readers (the notifier's
// per-send Current, the status endpoint) never race the writer (Set/Revert).
// Current and Status return plain values, never shared references, so a caller
// can't observe a torn or later-mutated string.
type WebhookConfig struct {
	mu       sync.RWMutex
	seed     string  // immutable boot value
	override *string // nil = no override; use seed
}

// Status is an immutable snapshot of the holder's effective state, safe to hand
// to a status endpoint or template. MaskedURL is the redacted (scheme+host)
// form of the effective URL and never carries the secret path/token — it is ""
// when no URL is configured (delivery off).
type Status struct {
	// Enabled is true when an effective URL is configured (delivery on).
	Enabled bool
	// MaskedURL is the redacted scheme+host of the effective URL, or "" when none.
	MaskedURL string
	// OverrideActive is true when a runtime override is shadowing the seed.
	OverrideActive bool
}

// NewWebhookConfig builds a holder seeded with the boot URL (possibly empty,
// which is the OFF state). The seed is immutable for the holder's lifetime.
func NewWebhookConfig(seed string) *WebhookConfig {
	return &WebhookConfig{seed: seed}
}

// Current returns the effective URL: the override if one is set, else the seed.
// It is read-locked and returns a value copy, safe for concurrent callers.
func (c *WebhookConfig) Current() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.override != nil {
		return *c.override
	}
	return c.seed
}

// Set installs a runtime override, shadowing the seed until Revert. An empty
// url is a valid override meaning "disable delivery" without losing the seed.
func (c *WebhookConfig) Set(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.override = &url
}

// Revert clears any runtime override, restoring the effective URL to the seed.
func (c *WebhookConfig) Revert() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.override = nil
}

// Enabled reports whether an effective URL is configured (delivery on).
func (c *WebhookConfig) Enabled() bool {
	return c.Current() != ""
}

// Status returns an immutable snapshot of the effective state for display. The
// read is taken under a single lock so Enabled/MaskedURL/OverrideActive are
// mutually consistent.
func (c *WebhookConfig) Status() Status {
	c.mu.RLock()
	defer c.mu.RUnlock()
	current := c.seed
	overrideActive := c.override != nil
	if overrideActive {
		current = *c.override
	}
	st := Status{OverrideActive: overrideActive}
	if current != "" {
		st.Enabled = true
		st.MaskedURL = redactURL(current)
	}
	return st
}
