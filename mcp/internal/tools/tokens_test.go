package tools

import (
	"testing"
	"time"
)

func TestStoreIssueVerifySuccess(t *testing.T) {
	s := NewStore()
	tok, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if tok == "" {
		t.Fatal("Issue returned empty token")
	}
	if !s.Verify("alice", tok) {
		t.Fatal("Verify: expected success for freshly issued token")
	}
}

func TestStoreVerifyWrongName(t *testing.T) {
	s := NewStore()
	tok, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if s.Verify("bob", tok) {
		t.Fatal("Verify: expected failure for a name the token wasn't issued to")
	}
}

func TestStoreVerifyWrongToken(t *testing.T) {
	s := NewStore()
	if _, err := s.Issue("alice"); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if s.Verify("alice", "not-the-real-token") {
		t.Fatal("Verify: expected failure for a mismatched token")
	}
}

func TestStoreVerifyExpired(t *testing.T) {
	s := NewStore()
	tok, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Same-package white-box test: reach into the unexported map to backdate
	// the expiry rather than sleeping tokenTTL (5 minutes) in a test.
	s.mu.Lock()
	entry := s.tokens["alice"]
	entry.expires = time.Now().Add(-time.Second)
	s.tokens["alice"] = entry
	s.mu.Unlock()

	if s.Verify("alice", tok) {
		t.Fatal("Verify: expected failure for an expired token")
	}
}

func TestStoreVerifySingleUse(t *testing.T) {
	s := NewStore()
	tok, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if !s.Verify("alice", tok) {
		t.Fatal("Verify: expected first use to succeed")
	}
	if s.Verify("alice", tok) {
		t.Fatal("Verify: expected second use of the same token to fail (single-use)")
	}
}

func TestStoreIssueMostRecentWins(t *testing.T) {
	s := NewStore()
	first, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue (first): %v", err)
	}
	second, err := s.Issue("alice")
	if err != nil {
		t.Fatalf("Issue (second): %v", err)
	}
	if first == second {
		t.Fatal("expected two distinct tokens from two Issue calls")
	}
	if s.Verify("alice", first) {
		t.Fatal("Verify: expected the prior token to be invalidated by re-issue")
	}
	if !s.Verify("alice", second) {
		t.Fatal("Verify: expected the most recently issued token to succeed")
	}
}
