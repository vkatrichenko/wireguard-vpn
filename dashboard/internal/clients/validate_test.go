package clients

import "testing"

// validPubKey is a real base64-encoded 32-byte WireGuard public key shape:
// 43 base64 chars + a single '=' pad.
const validPubKey = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopq="

func TestValidatePublicKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"valid", validPubKey, false},
		{"empty", "", true},
		{"too-short", "ABCDEF=", true},
		{"length-44-no-pad", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqr", true},
		{"bad-charset", "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmno!@#=", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidatePublicKey(tc.key)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidatePublicKey(%q) err = %v, wantErr %v", tc.key, err, tc.wantErr)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", "alice", false},
		{"valid-mixed", "alice.laptop_01-v2", false},
		{"empty", "", true},
		{"space", "alice laptop", true},
		{"slash", "alice/laptop", true},
		{"unicode", "álice", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateName(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateName(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
		})
	}
}

// TestValidateSet_EmptyIsValid pins the spec 017 contract that an empty
// bulk-replace payload is valid — it reconciles to zero peers, not an error.
func TestValidateSet_EmptyIsValid(t *testing.T) {
	t.Parallel()
	sn := mustParse(t, "172.16.15.1/24")

	if err := validateSet(sn, nil); err != nil {
		t.Errorf("validateSet(nil): want nil, got %v", err)
	}
	if err := validateSet(sn, []ReplaceEntry{}); err != nil {
		t.Errorf("validateSet([]): want nil, got %v", err)
	}
}

// TestValidateSet_MissingAddressRejected pins the bulk-path requirement that
// every entry carry an explicit, non-empty address — there is no
// auto-allocation fallback here (unlike Add), because the bulk path must be
// idempotent given the same input.
func TestValidateSet_MissingAddressRejected(t *testing.T) {
	t.Parallel()
	sn := mustParse(t, "172.16.15.1/24")

	entries := []ReplaceEntry{
		{Name: "alice", PublicKey: validPubKey, Address: ""},
	}
	if err := validateSet(sn, entries); err == nil {
		t.Error("validateSet with empty address: want error, got nil")
	}
}

// TestValidateSet_IntraPayloadDedup pins the new self-consistency checks: a
// duplicate name, public key, or address WITHIN the payload itself must be
// rejected, even though no single entry is individually invalid and neither
// entry conflicts with anything already in the (empty) table.
func TestValidateSet_IntraPayloadDedup(t *testing.T) {
	t.Parallel()
	sn := mustParse(t, "172.16.15.1/24")

	pubKeyB := "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
	pubKeyC := "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="

	tests := []struct {
		name    string
		entries []ReplaceEntry
	}{
		{
			name: "duplicate name",
			entries: []ReplaceEntry{
				{Name: "alice", PublicKey: validPubKey, Address: "172.16.15.5/32"},
				{Name: "alice", PublicKey: pubKeyB, Address: "172.16.15.6/32"},
			},
		},
		{
			name: "duplicate public key",
			entries: []ReplaceEntry{
				{Name: "alice", PublicKey: validPubKey, Address: "172.16.15.5/32"},
				{Name: "bob", PublicKey: validPubKey, Address: "172.16.15.6/32"},
			},
		},
		{
			name: "duplicate address",
			entries: []ReplaceEntry{
				{Name: "alice", PublicKey: validPubKey, Address: "172.16.15.5/32"},
				{Name: "bob", PublicKey: pubKeyB, Address: "172.16.15.5/32"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := validateSet(sn, tc.entries); err == nil {
				t.Errorf("validateSet(%s): want error, got nil", tc.name)
			}
		})
	}

	// Sanity check: three genuinely distinct entries must pass.
	ok := []ReplaceEntry{
		{Name: "alice", PublicKey: validPubKey, Address: "172.16.15.5/32"},
		{Name: "bob", PublicKey: pubKeyB, Address: "172.16.15.6/32"},
		{Name: "carol", PublicKey: pubKeyC, Address: "172.16.15.7/32"},
	}
	if err := validateSet(sn, ok); err != nil {
		t.Errorf("validateSet(distinct entries): want nil, got %v", err)
	}
}

func TestValidateAddress(t *testing.T) {
	t.Parallel()

	sn := mustParse(t, "172.16.15.1/24")

	tests := []struct {
		name    string
		addr    string
		wantErr bool
	}{
		{"valid", "172.16.15.6/32", false},
		{"empty", "", true},
		{"not-slash32", "172.16.15.6/24", true},
		{"out-of-subnet", "10.0.0.6/32", true},
		{"missing-prefix", "172.16.15.6", true},
		{"garbage", "abc.def.ghi.jkl/32", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateAddress(sn, tc.addr)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateAddress(%q) err = %v, wantErr %v", tc.addr, err, tc.wantErr)
			}
		})
	}
}
