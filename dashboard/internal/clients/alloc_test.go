package clients

import (
	"errors"
	"testing"

	"wireguard-dashboard/internal/wgconfig"
)

func mustParse(t *testing.T, s string) ServerNet {
	t.Helper()
	sn, err := ParseServerNet(s)
	if err != nil {
		t.Fatalf("ParseServerNet(%q): %v", s, err)
	}
	return sn
}

func TestParseServerNet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           string
		wantSubnet   string
		wantServerIP string
		wantErr      bool
	}{
		{"host-form", "172.16.15.1/24", "172.16.15.0/24", "172.16.15.1", false},
		{"fallback-empty", "", "172.16.15.0/24", "172.16.15.1", false},
		{"other-subnet", "10.0.0.1/16", "10.0.0.0/16", "10.0.0.1", false},
		{"ipv6", "fd00::1/64", "", "", true},
		{"garbage", "not-a-cidr", "", "", true},
		{"missing-prefix", "172.16.15.1", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sn, err := ParseServerNet(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got subnet=%v", sn.Subnet)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := sn.Subnet.String(); got != tc.wantSubnet {
				t.Errorf("subnet = %q, want %q", got, tc.wantSubnet)
			}
			if got := sn.ServerIP.String(); got != tc.wantServerIP {
				t.Errorf("serverIP = %q, want %q", got, tc.wantServerIP)
			}
		})
	}
}

// TestParseServerNet_FallbackMatchesConst pins the empty-string fallback to the
// wgconfig source-of-truth const.
func TestParseServerNet_FallbackMatchesConst(t *testing.T) {
	t.Parallel()
	fromEmpty := mustParse(t, "")
	fromConst := mustParse(t, wgconfig.DefaultServerNet)
	if fromEmpty.Subnet.String() != fromConst.Subnet.String() ||
		!fromEmpty.ServerIP.Equal(fromConst.ServerIP) {
		t.Fatalf("empty fallback %v/%v != const %v/%v",
			fromEmpty.Subnet, fromEmpty.ServerIP, fromConst.Subnet, fromConst.ServerIP)
	}
}

func TestAllocateAddress(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		net     string
		used    []string
		want    string
		wantErr error
	}{
		{
			name: "empty-subnet-lowest-after-server",
			net:  "172.16.15.1/24",
			used: nil,
			want: "172.16.15.2/32",
		},
		{
			name: "fragmented-gap-fill",
			net:  "172.16.15.1/24",
			used: []string{"172.16.15.2/32", "172.16.15.4/32"},
			want: "172.16.15.3/32",
		},
		{
			name: "sequential-after-contiguous",
			net:  "172.16.15.1/24",
			used: []string{"172.16.15.2/32", "172.16.15.3/32"},
			want: "172.16.15.4/32",
		},
		{
			name: "server-ip-reserved",
			net:  "172.16.15.1/24",
			// Even if the server IP is (wrongly) absent from used, it is never
			// handed out: lowest free is .2, never .1.
			used: nil,
			want: "172.16.15.2/32",
		},
		{
			name: "used-entries-without-prefix",
			net:  "172.16.15.1/24",
			used: []string{"172.16.15.2", "172.16.15.3"},
			want: "172.16.15.4/32",
		},
		{
			name:    "exhaustion-slash30",
			net:     "192.168.0.1/30", // usable hosts .1 (server) and .2
			used:    []string{"192.168.0.2/32"},
			want:    "",
			wantErr: ErrSubnetExhausted,
		},
		{
			name:    "exhaustion-slash31",
			net:     "192.168.0.1/31", // no usable hosts
			used:    nil,
			want:    "",
			wantErr: ErrSubnetExhausted,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sn := mustParse(t, tc.net)
			got, err := AllocateAddress(sn, tc.used)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("AllocateAddress = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateOverride(t *testing.T) {
	t.Parallel()

	sn := mustParse(t, "172.16.15.1/24")
	used := []string{"172.16.15.5/32"}

	tests := []struct {
		name    string
		addr    string
		want    string
		wantErr bool
	}{
		{"valid", "172.16.15.8/32", "172.16.15.8/32", false},
		{"out-of-subnet", "10.0.0.8/32", "", true},
		{"server-ip", "172.16.15.1/32", "", true},
		{"collision", "172.16.15.5/32", "", true},
		{"not-slash32", "172.16.15.8/24", "", true},
		{"malformed", "not-an-ip", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ValidateOverride(sn, tc.addr, used)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("ValidateOverride = %q, want %q", got, tc.want)
			}
		})
	}
}
