package wgconfig

import (
	"strings"
	"testing"
)

// TestResolverForVPC pins the VPC-CIDR -> resolver derivation (network base +
// 2) across several prefix lengths plus the error paths. The derivation must be
// correct for any block size AWS permits, not just the /16 the current VPC
// uses, because the whole point of deriving it is surviving a CIDR change.
func TestResolverForVPC(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cidr    string
		want    string
		wantErr bool
	}{
		{"slash16", "10.23.0.0/16", "10.23.0.2", false},
		{"slash20", "10.1.16.0/20", "10.1.16.2", false},
		{"slash24", "10.0.0.0/24", "10.0.0.2", false},
		{"different-octets", "172.31.0.0/16", "172.31.0.2", false},
		// A host address is normalised by ParseCIDR to its network base, so
		// the resolver is still base+2 regardless of the host bits supplied.
		{"host-bits-ignored", "10.5.0.37/16", "10.5.0.2", false},
		{"empty", "", "", true},
		{"garbage", "not-a-cidr", "", true},
		{"missing-prefix", "10.23.0.0", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolverForVPC(tc.cidr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ResolverForVPC(%q) = %q, want error", tc.cidr, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolverForVPC(%q) unexpected error: %v", tc.cidr, err)
			}
			if got != tc.want {
				t.Errorf("ResolverForVPC(%q) = %q, want %q", tc.cidr, got, tc.want)
			}
		})
	}
}

// TestBuild_Full asserts the exact, byte-stable full-tunnel config, including
// the literal placeholder, the derived DNS line, and that no real private key
// appears anywhere in the output.
func TestBuild_Full(t *testing.T) {
	t.Parallel()

	got, err := Build(
		Client{Name: "alice", Address: "172.16.15.6/32"},
		ModeFull,
		"SERVERPUBKEY0000000000000000000000000000000=",
		"203.0.113.1:51820",
		"10.23.0.2",
		[]string{"172.16.15.0/24", "10.23.0.0/16"},
	)
	if err != nil {
		t.Fatalf("Build returned unexpected error: %v", err)
	}

	want := "[Interface]\n" +
		"PrivateKey = <paste your client private key here>\n" +
		"Address = 172.16.15.6/32\n" +
		"DNS = 10.23.0.2\n" +
		"\n" +
		"[Peer]\n" +
		"PublicKey = SERVERPUBKEY0000000000000000000000000000000=\n" +
		"Endpoint = 203.0.113.1:51820\n" +
		"AllowedIPs = 0.0.0.0/0, ::/0\n" +
		"PersistentKeepalive = 25\n"

	if got != want {
		t.Errorf("Build mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}

	// The placeholder must be present and no real key value substituted.
	if !strings.Contains(got, privateKeyPlaceholder) {
		t.Errorf("output missing the PrivateKey placeholder:\n%s", got)
	}
}

// TestOverlaySubnet pins the server-host CIDR -> overlay network derivation
// across the default /24, a non-.1 host, and a non-/24 prefix, plus the error
// path. The handler derives the split-tunnel overlay from WG_SERVER_NET, so
// this must be correct whatever subnet the operator chose.
func TestOverlaySubnet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		serverNet string
		want      string
		wantErr   bool
	}{
		{"default-slash24", "172.16.15.1/24", "172.16.15.0/24", false},
		{"non-dot1-host", "172.16.15.5/24", "172.16.15.0/24", false},
		{"slash22", "10.8.0.5/22", "10.8.0.0/22", false},
		{"slash16", "10.0.4.1/16", "10.0.0.0/16", false},
		{"empty", "", "", true},
		{"garbage", "not-a-cidr", "", true},
		{"missing-prefix", "172.16.15.1", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := OverlaySubnet(tc.serverNet)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("OverlaySubnet(%q) = %q, want error", tc.serverNet, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("OverlaySubnet(%q) unexpected error: %v", tc.serverNet, err)
			}
			if got != tc.want {
				t.Errorf("OverlaySubnet(%q) = %q, want %q", tc.serverNet, got, tc.want)
			}
		})
	}
}

// TestBuild_Split asserts the split-tunnel config and proves the ONLY
// difference from the full-tunnel output is the AllowedIPs line: the
// caller-supplied split routes (WG overlay plus the VPC CIDR), joined with
// ", ".
func TestBuild_Split(t *testing.T) {
	t.Parallel()

	client := Client{Name: "alice", Address: "172.16.15.6/32"}
	const (
		key      = "SERVERPUBKEY0000000000000000000000000000000="
		endpoint = "203.0.113.1:51820"
		dns      = "10.23.0.2"
	)
	splitRoutes := []string{"172.16.15.0/24", "10.23.0.0/16"}

	split, err := Build(client, ModeSplit, key, endpoint, dns, splitRoutes)
	if err != nil {
		t.Fatalf("Build(split) unexpected error: %v", err)
	}
	if !strings.Contains(split, "AllowedIPs = 172.16.15.0/24, 10.23.0.0/16\n") {
		t.Errorf("split output missing the expected AllowedIPs line:\n%s", split)
	}

	// Diff invariant: full and split differ only on the AllowedIPs line.
	full, err := Build(client, ModeFull, key, endpoint, dns, splitRoutes)
	if err != nil {
		t.Fatalf("Build(full) unexpected error: %v", err)
	}
	normalize := func(s string) string {
		return strings.Replace(s, "AllowedIPs = 172.16.15.0/24, 10.23.0.0/16\n", "AllowedIPs = 0.0.0.0/0, ::/0\n", 1)
	}
	if normalize(split) != full {
		t.Errorf("split vs full differ in more than AllowedIPs:\n--- split (normalized) ---\n%s\n--- full ---\n%s", normalize(split), full)
	}
}

// TestParseMode pins the query-string mapping: only the exact "split" selects
// split-tunnel; everything else (empty, "full", garbage) defaults to full.
func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want Mode
	}{
		{"split", ModeSplit},
		{"full", ModeFull},
		{"", ModeFull},
		{"garbage", ModeFull},
		{"Split", ModeFull}, // case-sensitive — only lowercase "split" matches
	}
	for _, tc := range tests {
		if got := ParseMode(tc.in); got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuild_UnsupportedMode proves an unknown routing mode is rejected. Only
// the full and split profiles are valid; any other Mode value errors.
func TestBuild_UnsupportedMode(t *testing.T) {
	t.Parallel()

	if _, err := Build(Client{Address: "172.16.15.6/32"}, Mode("bogus"), "k", "h:1", "1.1.1.1", []string{"172.16.15.0/24"}); err == nil {
		t.Fatal("Build with unsupported mode returned nil error, want non-nil")
	}
}

// TestBuild_SplitOverlayOnly proves the off-AWS shape: a single overlay route
// (no VPC) plus the WG_CLIENT_DNS resolver passed through verbatim.
func TestBuild_SplitOverlayOnly(t *testing.T) {
	t.Parallel()

	got, err := Build(
		Client{Name: "bob", Address: "172.16.15.7/32"},
		ModeSplit,
		"SERVERPUBKEY0000000000000000000000000000000=",
		"198.51.100.9:51820",
		"1.1.1.1",
		[]string{"172.16.15.0/24"},
	)
	if err != nil {
		t.Fatalf("Build returned unexpected error: %v", err)
	}
	if !strings.Contains(got, "DNS = 1.1.1.1\n") {
		t.Errorf("output missing the off-AWS DNS line:\n%s", got)
	}
	if !strings.Contains(got, "AllowedIPs = 172.16.15.0/24\n") {
		t.Errorf("output missing the overlay-only AllowedIPs line:\n%s", got)
	}
	if strings.Contains(got, "10.23.0.0/16") {
		t.Errorf("overlay-only split unexpectedly contains a VPC CIDR:\n%s", got)
	}
}

// TestBuildServerPeer pins the single-[Peer] stanza fields and the optional
// leading name comment.
func TestBuildServerPeer(t *testing.T) {
	t.Parallel()

	got := BuildServerPeer(ServerPeer{
		Name:      "alice",
		PublicKey: "PUBKEYALICE",
		Address:   "172.16.15.2/32",
		Enabled:   true,
	})
	want := "# alice\n" +
		"[Peer]\n" +
		"PublicKey = PUBKEYALICE\n" +
		"AllowedIPs = 172.16.15.2/32\n" +
		"PersistentKeepalive = 25\n"
	if got != want {
		t.Errorf("BuildServerPeer =\n%q\nwant\n%q", got, want)
	}

	// An empty name omits the comment line entirely.
	got = BuildServerPeer(ServerPeer{PublicKey: "K", Address: "172.16.15.3/32", Enabled: true})
	if strings.HasPrefix(got, "#") {
		t.Errorf("empty-name peer rendered a comment line:\n%q", got)
	}
}

// TestBuildServerConf checks the [Interface] block, that disabled peers are
// omitted, and that enabled peers render in ascending-address order regardless
// of input order.
func TestBuildServerConf(t *testing.T) {
	t.Parallel()

	iface := ServerInterface{
		Address:    "172.16.15.1/24",
		ListenPort: 51820,
		PrivateKey: "SERVERPRIV",
		PostUp:     []string{"iptables -A FORWARD -i wg0 -j ACCEPT"},
		PostDown:   []string{"iptables -D FORWARD -i wg0 -j ACCEPT"},
	}
	peers := []ServerPeer{
		{Name: "carol", PublicKey: "KC", Address: "172.16.15.10/32", Enabled: true},
		{Name: "alice", PublicKey: "KA", Address: "172.16.15.2/32", Enabled: true},
		{Name: "mallory", PublicKey: "KM", Address: "172.16.15.4/32", Enabled: false},
		{Name: "bob", PublicKey: "KB", Address: "172.16.15.3/32", Enabled: true},
	}

	want := "[Interface]\n" +
		"Address = 172.16.15.1/24\n" +
		"ListenPort = 51820\n" +
		"PrivateKey = SERVERPRIV\n" +
		"PostUp = iptables -A FORWARD -i wg0 -j ACCEPT\n" +
		"PostDown = iptables -D FORWARD -i wg0 -j ACCEPT\n" +
		"\n# alice\n[Peer]\nPublicKey = KA\nAllowedIPs = 172.16.15.2/32\nPersistentKeepalive = 25\n" +
		"\n# bob\n[Peer]\nPublicKey = KB\nAllowedIPs = 172.16.15.3/32\nPersistentKeepalive = 25\n" +
		"\n# carol\n[Peer]\nPublicKey = KC\nAllowedIPs = 172.16.15.10/32\nPersistentKeepalive = 25\n"

	got := BuildServerConf(iface, peers)
	if got != want {
		t.Errorf("BuildServerConf =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "mallory") || strings.Contains(got, "KM") {
		t.Errorf("disabled peer leaked into config:\n%q", got)
	}
}

// TestBuildServerConf_StableOrdering proves the .10-before-.2 ordering bug is
// not present: numeric IP order, not lexicographic string order.
func TestBuildServerConf_StableOrdering(t *testing.T) {
	t.Parallel()

	peers := []ServerPeer{
		{PublicKey: "K10", Address: "172.16.15.10/32", Enabled: true},
		{PublicKey: "K2", Address: "172.16.15.2/32", Enabled: true},
	}
	got := BuildServerConf(ServerInterface{Address: "172.16.15.1/24"}, peers)
	i2 := strings.Index(got, "172.16.15.2/32")
	i10 := strings.Index(got, "172.16.15.10/32")
	if i2 < 0 || i10 < 0 || i2 > i10 {
		t.Errorf(".2 must render before .10; got positions %d and %d:\n%s", i2, i10, got)
	}
}
