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
