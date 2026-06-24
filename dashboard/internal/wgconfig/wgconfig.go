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
	"strings"
)

const (
	// wgTunnelSubnet is the WireGuard overlay network the peers share. It is
	// the /24 derived from `wg_server_net` (172.16.15.1/24) in
	// terraform/modules/wireguard; if that ever changes, this constant must
	// move with it. Used as the first AllowedIPs entry in split-tunnel mode
	// (added in a later slice).
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
