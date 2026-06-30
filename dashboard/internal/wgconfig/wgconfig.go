// Package wgconfig assembles a wg-quick client configuration file from the
// facts the dashboard already knows about the running server, leaving exactly
// one field — the client's PrivateKey — as a placeholder for the operator to
// fill in locally.
//
// The package is deliberately pure: Build performs no I/O, shells out to
// nothing, and reads no environment. Every server-derived input (the server's
// public key, the public endpoint, the VPC CIDR) is passed in as a plain
// string by the caller, which is what makes the output exactly reproducible
// and trivially table-testable. The handler in internal/server is responsible
// for gathering those inputs (clientsfile + serverinfo) and for turning a
// missing input into the right HTTP status.
//
// Security note: the produced file contains no secret. The PrivateKey line is
// a literal placeholder, never a real key — the server never holds client
// private keys (they are generated off-host with `wg genkey`). The remaining
// values (server public key, public endpoint, the client's assigned tunnel
// address) are all non-secret.
package wgconfig

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

const (
	// DefaultServerNet is the WG_SERVER_NET fallback (server-host form:
	// host + prefix) used when the environment supplies none. It is the single
	// source of truth for the fallback overlay; wgTunnelSubnet below is the /24
	// derived from it. Mirrors `wg_server_net` in terraform/modules/wireguard;
	// if that ever changes, this constant must move with it.
	DefaultServerNet = "172.16.15.1/24"

	// wgTunnelSubnet is the WireGuard overlay network the peers share. It is
	// the /24 derived from DefaultServerNet (172.16.15.1/24). Used as the first
	// AllowedIPs entry in split-tunnel mode (added in a later slice).
	wgTunnelSubnet = "172.16.15.0/24"

	// privateKeyPlaceholder is the literal value emitted on the Interface's
	// PrivateKey line. It is intentionally NOT a valid key, so a config shipped
	// without the operator replacing it fails loudly at `wg-quick up` rather
	// than silently bringing up a broken tunnel.
	privateKeyPlaceholder = "<paste your client private key here>"

	// persistentKeepalive (seconds) keeps NAT mappings warm for clients behind
	// NAT. 25s is the WireGuard community default.
	persistentKeepalive = 25

	// fullTunnelAllowedIPs routes all client traffic (v4 + v6) through the VPN
	// — exit-node behaviour. The server's masquerade/forward rules (confirmed
	// in the wireguard module user-data) make this actually route.
	fullTunnelAllowedIPs = "0.0.0.0/0, ::/0"
)

// Mode selects the routing profile baked into the generated config. Only the
// AllowedIPs line differs between modes; every other field is identical.
type Mode string

const (
	// ModeFull is exit-node routing: all traffic through the tunnel.
	ModeFull Mode = "full"

	// ModeSplit routes only the WireGuard overlay plus the AWS VPC — the
	// client reaches peers and private VPC resources (and the VPC DNS
	// resolver) while its local internet stays off the tunnel.
	ModeSplit Mode = "split"
)

// ParseMode maps a raw query-string value to a Mode, defaulting to ModeFull
// for anything it doesn't recognise (including the empty string and "full").
// Only the exact literal "split" selects split-tunnel — keeping the default
// the safe, all-traffic-protected profile.
func ParseMode(s string) Mode {
	switch s {
	case string(ModeSplit):
		return ModeSplit
	default:
		return ModeFull
	}
}

// Client carries the per-client manifest fields the config needs. It is a
// narrow value type (not the richer clientsfile.Client) so wgconfig imports
// nothing internal and stays a leaf package; the handler maps at the boundary.
type Client struct {
	Name    string
	Address string // e.g. "172.16.15.6/32"
}

// Build renders the wg-quick client config for the given client and routing
// mode. serverPubKey is the server's WireGuard public key, endpoint is the
// reachable "host:port" the client dials, and vpcCIDR is the VPC's primary
// IPv4 CIDR used to derive the DNS resolver.
//
// The function is pure and deterministic: identical inputs always yield an
// identical, byte-stable string. It returns an error only when vpcCIDR cannot
// be parsed or the mode is unsupported — never for empty server inputs, which
// the caller is expected to have validated (and to surface as a 503).
func Build(client Client, mode Mode, serverPubKey, endpoint, vpcCIDR string) (string, error) {
	dns, err := resolverFor(vpcCIDR)
	if err != nil {
		return "", err
	}

	var allowedIPs string
	switch mode {
	case ModeFull:
		allowedIPs = fullTunnelAllowedIPs
	case ModeSplit:
		// WireGuard overlay first, then the VPC CIDR so the client routes
		// private AWS resources and the VPC DNS resolver through the tunnel.
		allowedIPs = wgTunnelSubnet + ", " + vpcCIDR
	default:
		return "", fmt.Errorf("wgconfig: unsupported mode %q", mode)
	}

	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "PrivateKey = %s\n", privateKeyPlaceholder)
	fmt.Fprintf(&b, "Address = %s\n", client.Address)
	fmt.Fprintf(&b, "DNS = %s\n", dns)
	b.WriteString("\n")
	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", serverPubKey)
	fmt.Fprintf(&b, "Endpoint = %s\n", endpoint)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", allowedIPs)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", persistentKeepalive)
	return b.String(), nil
}

// resolverFor derives the AWS VPC DNS resolver address from a VPC CIDR. The
// Amazon-provided resolver always lives at the VPC's network base address plus
// two (e.g. 10.23.0.0/16 -> 10.23.0.2). Deriving it — rather than hardcoding a
// single value — keeps the generated config correct if the VPC CIDR ever
// changes.
//
// Only IPv4 VPC CIDRs are supported; an unparseable or non-IPv4 input returns
// an error so the caller can surface a 503 rather than emit a config with a
// wrong DNS line.
func resolverFor(vpcCIDR string) (string, error) {
	_, ipNet, err := net.ParseCIDR(vpcCIDR)
	if err != nil {
		return "", fmt.Errorf("wgconfig: parse vpc cidr %q: %w", vpcCIDR, err)
	}
	base := ipNet.IP.To4()
	if base == nil {
		return "", fmt.Errorf("wgconfig: vpc cidr %q is not IPv4", vpcCIDR)
	}
	resolver := make(net.IP, len(base))
	copy(resolver, base)
	addToIP(resolver, 2)
	return resolver.String(), nil
}

// ServerPeer carries the per-client facts needed to render one [Peer] stanza
// in the server's wg0.conf. It is a narrow value type (not db.Client) so
// wgconfig stays a leaf package; the orchestration layer maps at the boundary.
//
// Enabled is honoured by BuildServerConf, which omits disabled peers entirely
// (the client is retained in the DB but not in the live config). Name is
// emitted only as a leading comment for operator readability and never affects
// the peer's identity.
type ServerPeer struct {
	Name      string
	PublicKey string
	Address   string // the client's /32, e.g. "172.16.15.6/32"
	Enabled   bool
}

// ServerInterface carries the [Interface] facts for the server's wg0.conf.
//
// PostUp/PostDown are caller-supplied because the NAT/forwarding rules depend
// on the host's egress interface name, which wgconfig cannot know purely. The
// host helper (a later slice) gathers them and passes them in, keeping this
// package free of any I/O or environment reads. Each entry renders as its own
// PostUp =/PostDown = line in order.
type ServerInterface struct {
	Address    string // server's tunnel address, e.g. "172.16.15.1/24"
	ListenPort int
	PrivateKey string
	PostUp     []string
	PostDown   []string
}

// BuildServerPeer renders a single [Peer] block for the server's wg0.conf. It
// is pure and byte-stable. The Name, when non-empty, is emitted as a leading
// "# name" comment above the stanza.
func BuildServerPeer(p ServerPeer) string {
	var b strings.Builder
	if p.Name != "" {
		fmt.Fprintf(&b, "# %s\n", p.Name)
	}
	b.WriteString("[Peer]\n")
	fmt.Fprintf(&b, "PublicKey = %s\n", p.PublicKey)
	fmt.Fprintf(&b, "AllowedIPs = %s\n", p.Address)
	fmt.Fprintf(&b, "PersistentKeepalive = %d\n", persistentKeepalive)
	return b.String()
}

// BuildServerConf renders the full /etc/wireguard/wg0.conf from the server
// interface facts and the set of clients. Only enabled peers are rendered, in
// deterministic order (ascending by tunnel address) so identical inputs always
// yield a byte-stable file — which is what makes the downstream `wg syncconf`
// diff stable and the renderer trivially table-testable.
//
// The function is pure: every host-specific input (private key, listen port,
// PostUp/PostDown NAT lines) is passed in by the caller.
func BuildServerConf(iface ServerInterface, peers []ServerPeer) string {
	var b strings.Builder
	b.WriteString("[Interface]\n")
	fmt.Fprintf(&b, "Address = %s\n", iface.Address)
	fmt.Fprintf(&b, "ListenPort = %d\n", iface.ListenPort)
	fmt.Fprintf(&b, "PrivateKey = %s\n", iface.PrivateKey)
	for _, line := range iface.PostUp {
		fmt.Fprintf(&b, "PostUp = %s\n", line)
	}
	for _, line := range iface.PostDown {
		fmt.Fprintf(&b, "PostDown = %s\n", line)
	}

	for _, p := range enabledSortedByAddress(peers) {
		b.WriteString("\n")
		b.WriteString(BuildServerPeer(p))
	}
	return b.String()
}

// BuildServerPeers renders ONLY the [Peer] stanzas — no [Interface] block — for
// the enabled clients, in the same deterministic ascending-address order as
// BuildServerConf (stanzas separated by a blank line, byte-stable for identical
// input). It exists for the unprivileged live-apply path (internal/wgsync):
// that process runs as the dashboard user, cannot read the 0600-root wg0.conf,
// and therefore must NEVER hold the server private key. It stages this
// peers-only fragment and lets the root wg-sync helper merge it with the
// on-disk [Interface] block. Use BuildServerConf only where the private key is
// legitimately available (it embeds it); use this everywhere it is not.
func BuildServerPeers(peers []ServerPeer) string {
	var b strings.Builder
	for i, p := range enabledSortedByAddress(peers) {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(BuildServerPeer(p))
	}
	return b.String()
}

// enabledSortedByAddress filters to enabled peers and returns them sorted by
// tunnel address. Sorting on the parsed IP (not the string) keeps .2 before
// .10; unparseable addresses sort last by raw string so a malformed entry
// never panics the renderer.
func enabledSortedByAddress(peers []ServerPeer) []ServerPeer {
	out := make([]ServerPeer, 0, len(peers))
	for _, p := range peers {
		if p.Enabled {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return addrLess(out[i].Address, out[j].Address)
	})
	return out
}

func addrLess(a, b string) bool {
	ai, aok := addrSortKey(a)
	bi, bok := addrSortKey(b)
	if aok != bok {
		return aok // parseable addresses sort before unparseable ones
	}
	if !aok {
		return a < b
	}
	return ai < bi
}

func addrSortKey(s string) (uint32, bool) {
	host := s
	if i := strings.IndexByte(s, '/'); i >= 0 {
		host = s[:i]
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return 0, false
	}
	return uint32(ip[0])<<24 | uint32(ip[1])<<16 | uint32(ip[2])<<8 | uint32(ip[3]), true
}

// addToIP adds n to the integer value of ip in place, propagating carry from
// the least-significant octet upward. For the VPC CIDRs AWS permits (/16–/28)
// the base+2 never carries past the last octet, but the general form keeps the
// helper correct for any block size.
func addToIP(ip net.IP, n int) {
	for i := len(ip) - 1; i >= 0 && n > 0; i-- {
		sum := int(ip[i]) + n
		ip[i] = byte(sum & 0xff)
		n = sum >> 8
	}
}
