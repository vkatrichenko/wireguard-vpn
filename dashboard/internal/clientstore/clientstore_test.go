package clientstore

import (
	"encoding/json"
	"testing"
)

// TestCanonical_SortsByAddressString is the load-bearing regression test for
// the exact bug class spec 017's clients_sorted logic existed to avoid:
// Terraform's sort() over clients_config keys is a plain lexicographic STRING
// sort, so "172.16.15.10/32" sorts BEFORE "172.16.15.6/32" (byte '1' < byte
// '6') even though 10 > 6 numerically. Canonical must reproduce that exactly,
// NOT an IP-aware sort, or the Go and Terraform sides would disagree and the
// drift `check` would false-positive forever.
func TestCanonical_SortsByAddressString(t *testing.T) {
	in := []Entry{
		{Name: "six", Address: "172.16.15.6/32", PublicKey: "BBBB"},
		{Name: "ten", Address: "172.16.15.10/32", PublicKey: "AAAA"},
		{Name: "two", Address: "172.16.15.2/32", PublicKey: "CCCC"},
	}
	body, err := Canonical(in)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	var out []Entry
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	wantOrder := []string{"172.16.15.10/32", "172.16.15.2/32", "172.16.15.6/32"}
	for i, want := range wantOrder {
		if out[i].Address != want {
			t.Errorf("out[%d].Address = %q, want %q (full order: %+v)", i, out[i].Address, want, out)
		}
	}
}

// TestCanonical_FieldSubset asserts the JSON output carries ONLY name,
// address, public_key — no dashboard-only field ever leaks into the bridge
// object, which is what keeps an enable/disable toggle from registering as
// Terraform drift.
func TestCanonical_FieldSubset(t *testing.T) {
	body, err := Canonical([]Entry{{Name: "alice", Address: "172.16.15.2/32", PublicKey: "AAAA"}})
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(raw) != 1 {
		t.Fatalf("len(raw) = %d, want 1", len(raw))
	}
	if len(raw[0]) != 3 {
		t.Fatalf("field count = %d, want 3 (got keys %+v)", len(raw[0]), raw[0])
	}
	for _, key := range []string{"name", "address", "public_key"} {
		if _, ok := raw[0][key]; !ok {
			t.Errorf("missing expected field %q in %+v", key, raw[0])
		}
	}
}

// TestCanonical_EmptyEncodesAsEmptyArray matches Terraform's jsonencode([])
// for a zero-client set — must be "[]", never the JSON literal null (which a
// naive nil-slice json.Marshal would produce and which jsondecode-based
// drift comparison would NOT treat as equal to "[]").
func TestCanonical_EmptyEncodesAsEmptyArray(t *testing.T) {
	body, err := Canonical(nil)
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	if string(body) != "[]" {
		t.Errorf("Canonical(nil) = %q, want %q", body, "[]")
	}
}

// TestCanonical_DoesNotMutateInput guards against a Canonical implementation
// that sorts the caller's backing array in place — callers (internal/clients)
// pass a freshly-built slice today, but a future caller reusing a slice
// across calls must not see it silently reordered.
func TestCanonical_DoesNotMutateInput(t *testing.T) {
	in := []Entry{
		{Name: "b", Address: "172.16.15.9/32"},
		{Name: "a", Address: "172.16.15.2/32"},
	}
	orig := append([]Entry(nil), in...)
	if _, err := Canonical(in); err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	for i := range in {
		if in[i] != orig[i] {
			t.Errorf("Canonical mutated input at index %d: got %+v, want %+v", i, in[i], orig[i])
		}
	}
}
