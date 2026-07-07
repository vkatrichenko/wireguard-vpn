// Delete is the sole irreversible /api/clients* operation (add/edit/enable/
// disable are all reversible with another call; a removed peer's keypair and
// history are gone for good), so per the owner's 2026-07-06 confirmation-gate
// resolution it gets a harder gate than the inline `confirm:true` param used
// everywhere else: a two-call, token-gated dry-run flow. preview_delete_client
// (mutating.go) issues a token via Store.Issue; delete_client redeems it via
// Store.Verify before calling DELETE. This file is the token bookkeeping only
// — no HTTP, no tool registration.
package tools

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

// tokenTTL is how long a preview_delete_client token stays redeemable. Five
// minutes is long enough for an LLM agent (or the operator reading its
// output) to read the preview and decide, but short enough that a token
// idling in an old conversation transcript can't be replayed against a peer
// that's since been renamed/recreated out from under it.
const tokenTTL = 5 * time.Minute

// tokenEntry binds one issued token to the peer name it was issued for, plus
// its expiry. Store never needs the peer's other fields (public key,
// enabled, etc.) — only preview_delete_client's response body carries those,
// and it re-fetches them fresh at preview time rather than caching anything
// here that could go stale.
type tokenEntry struct {
	token   string
	expires time.Time
}

// Store is the in-process, in-memory delete-confirmation token bookkeeper
// for one mcp-server run. It intentionally holds no state beyond a single
// process's lifetime: the MCP server is a short-lived stdio subprocess (see
// mcp/cmd/mcp-server/main.go and project-context/routes/mcp-server/README.md)
// spawned fresh per MCP host session, so a token that doesn't survive a
// restart is a feature, not a gap — there is no host-restart-then-replay
// window to worry about.
//
// Binding is per peer NAME, not per public key or any other identity: a
// token issued for "alice" only ever redeems a delete of "alice". Renaming
// the peer between preview and delete (via edit_client) invalidates the
// token as a side effect, since Verify looks up by the post-rename name.
//
// Every token is single-use: Verify deletes the entry on a successful match,
// so a captured/replayed token can't be redeemed twice. Re-issuing a token
// for a name that already has one live OVERWRITES it (most-recent-wins) —
// there is deliberately no "already has a pending token" error, since the
// natural LLM-agent retry pattern (call preview again if unsure) should just
// work rather than requiring the caller to track token lifecycle itself.
type Store struct {
	mu     sync.Mutex
	tokens map[string]tokenEntry
}

// NewStore constructs an empty Store. Call once at startup in
// cmd/mcp-server/main.go and share the instance across every tool
// invocation, mirroring how internal/dashboard.Client is constructed once
// and shared (see that package's doc comment) — a per-call Store would defeat
// the whole point, since a token issued by one call would never be visible
// to the next.
func NewStore() *Store {
	return &Store{tokens: make(map[string]tokenEntry)}
}

// Issue mints a fresh, cryptographically random token bound to name, valid
// for tokenTTL, and returns it. Any prior token for the same name is
// discarded (most-recent-wins — see the Store doc comment).
func (s *Store) Issue(name string) (string, error) {
	// 32 random bytes (256 bits) hex-encoded: comfortably beyond any
	// brute-force concern for a 5-minute-lived, single-use, in-memory-only
	// secret — this is a confirmation nonce, not a cryptographic key, but
	// there's no reason to skimp given crypto/rand is free.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating delete-confirmation token: %w", err)
	}
	token := hex.EncodeToString(buf)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[name] = tokenEntry{token: token, expires: time.Now().Add(tokenTTL)}
	return token, nil
}

// Verify reports whether token is the current, unexpired token issued for
// name. On a successful match the entry is consumed (deleted) so the same
// token can never redeem a second delete — this is what makes the gate
// single-use rather than a standing bearer credential.
//
// A missing name, an expired entry (also deleted here as a cheap opportunistic
// sweep — there is no background GC goroutine since the process is
// short-lived and the token count is bounded by distinct peer names touched
// in one session), or a token mismatch all report false without mutating
// anything else.
//
// The token comparison uses crypto/subtle.ConstantTimeCompare rather than
// `==` so a mistyped/guessed token can't be distinguished from a
// wrong-length one by response-timing side channel — defense in depth for a
// value that, unlike a network-facing secret, is only ever compared against
// input from the same trust boundary (the calling LLM/operator), but costs
// nothing to harden.
func (s *Store) Verify(name, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.tokens[name]
	if !ok {
		return false
	}
	if time.Now().After(entry.expires) {
		delete(s.tokens, name)
		return false
	}
	match := subtle.ConstantTimeCompare([]byte(entry.token), []byte(token)) == 1
	if match {
		delete(s.tokens, name)
	}
	return match
}
