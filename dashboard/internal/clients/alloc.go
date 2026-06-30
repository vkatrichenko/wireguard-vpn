// Package clients holds the pure orchestration logic for runtime client
// management: parsing the server network, allocating tunnel addresses, and
// validating operator-supplied client fields.
//
// Everything in this slice is deliberately pure — no DB, no HTTP, no I/O, no
// environment reads. The already-used address set is passed in by the caller
// (the stateful service added in a later slice owns the DB and the write
// mutex), which is what makes the allocator and validators byte-stable and
// trivially table-testable. The WG_SERVER_NET value is likewise passed in to
// ParseServerNet rather than read here.
package clients

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"wireguard-dashboard/internal/wgconfig"
)

// ErrSubnetExhausted is returned by AllocateAddress when no free host /32
// remains in the subnet.
var ErrSubnetExhausted = errors.New("clients: server subnet exhausted")

// ServerNet is the parsed WG_SERVER_NET: the overlay subnet plus the reserved
// server host IP within it. The server IP is never handed out to a client.
type ServerNet struct {
	Subnet   *net.IPNet
	ServerIP net.IP // 4-byte form
}

// ParseServerNet parses a WG_SERVER_NET value of the server-host form
// "host/prefix" (e.g. "172.16.15.1/24") into the subnet and the reserved
// server host IP. An empty string falls back to wgconfig.DefaultServerNet,
// keeping that const the single source of truth for the fallback overlay.
//
// Only IPv4 is supported; a non-IPv4 or unparseable value returns an error.
func ParseServerNet(s string) (ServerNet, error) {
	if s == "" {
		s = wgconfig.DefaultServerNet
	}
	ip, ipNet, err := net.ParseCIDR(s)
	if err != nil {
		return ServerNet{}, fmt.Errorf("clients: parse server net %q: %w", s, err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return ServerNet{}, fmt.Errorf("clients: server net %q is not IPv4", s)
	}
	return ServerNet{Subnet: ipNet, ServerIP: ip4}, nil
}

// AllocateAddress returns the lowest free host /32 in sn.Subnet, as a string
// like "172.16.15.8/32". The server IP and every address in used are excluded,
// as are the network and broadcast addresses. Fragmented gaps are filled with
// the lowest free host. When no host remains, it returns ErrSubnetExhausted.
//
// Entries in used may be either "ip" or "ip/32"; unparseable entries are
// ignored (the validators are responsible for rejecting bad input upstream).
func AllocateAddress(sn ServerNet, used []string) (string, error) {
	network, broadcast, err := v4Bounds(sn.Subnet)
	if err != nil {
		return "", err
	}
	reserved := reservedSet(sn, used)

	// Usable hosts are network+1 .. broadcast-1. Guard the unsigned subtraction
	// for tiny prefixes (/31, /32) where no usable host exists.
	if broadcast <= network+1 {
		return "", ErrSubnetExhausted
	}
	for h := network + 1; h <= broadcast-1; h++ {
		if !reserved[h] {
			return uint32ToIP(h).String() + "/32", nil
		}
	}
	return "", ErrSubnetExhausted
}

// reservedSet collects the addresses that must not be allocated: the server IP
// plus every parseable entry in used.
func reservedSet(sn ServerNet, used []string) map[uint32]bool {
	reserved := make(map[uint32]bool, len(used)+1)
	if v, ok := ipToUint32(sn.ServerIP); ok {
		reserved[v] = true
	}
	for _, u := range used {
		if v, ok := parseHostUint32(u); ok {
			reserved[v] = true
		}
	}
	return reserved
}

// v4Bounds returns the network base and broadcast addresses of an IPv4 subnet
// as uint32. A non-IPv4 subnet is an error.
func v4Bounds(subnet *net.IPNet) (network, broadcast uint32, err error) {
	if subnet == nil {
		return 0, 0, errors.New("clients: nil subnet")
	}
	base := subnet.IP.To4()
	if base == nil {
		return 0, 0, fmt.Errorf("clients: subnet %s is not IPv4", subnet)
	}
	ones, bits := subnet.Mask.Size()
	if bits != 32 {
		return 0, 0, fmt.Errorf("clients: subnet %s is not IPv4", subnet)
	}
	network = uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
	size := uint32(1) << (32 - ones)
	broadcast = network + size - 1
	return network, broadcast, nil
}

func ipToUint32(ip net.IP) (uint32, bool) {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0, false
	}
	return uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3]), true
}

func uint32ToIP(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// parseHostUint32 parses an "ip" or "ip/32" string into its uint32 value.
func parseHostUint32(s string) (uint32, bool) {
	host := s
	if i := strings.IndexByte(s, '/'); i >= 0 {
		host = s[:i]
	}
	return ipToUint32(net.ParseIP(host))
}
