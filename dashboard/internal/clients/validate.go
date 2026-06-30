package clients

import (
	"fmt"
	"net"
	"regexp"
)

// Validation rules mirror the terraform wireguard module's clients_config
// validations (public_key length 44, address ^[0-9.]+/32$) and the dashboard's
// sanitizeFilename charset intent, so a client accepted here is also a legal
// Terraform seed entry and a safe config filename.
var (
	// pubKeyRe matches a base64-encoded 32-byte WireGuard key: 44 characters,
	// the last of which is the single '=' pad byte that a 32-byte payload
	// always produces. Stricter than a bare length==44 check, and rejects any
	// non-base64 character.
	pubKeyRe = regexp.MustCompile(`^[A-Za-z0-9+/]{43}=$`)

	// addrRe matches the IPv4 /32 CIDR form, matching the terraform validation
	// (`^[0-9.]+/32$`). Structural validity (a real IPv4) is checked separately
	// via net.ParseCIDR.
	addrRe = regexp.MustCompile(`^[0-9.]+/32$`)

	// nameRe matches the wg/filename-safe charset: ASCII letters, digits, dot,
	// underscore, hyphen. Mirrors sanitizeFilename's pass-through set so a valid
	// name never has to be mangled into a download filename.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
)

// ValidatePublicKey checks that key is a 44-char base64 WireGuard public key.
func ValidatePublicKey(key string) error {
	if !pubKeyRe.MatchString(key) {
		return fmt.Errorf("clients: public key must be a 44-character base64 WireGuard key")
	}
	return nil
}

// ValidateName checks that name is non-empty and uses only the wg/filename-safe
// charset.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("clients: name must not be empty")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("clients: name %q may use only letters, digits, '.', '_' and '-'", name)
	}
	return nil
}

// ValidateAddress checks that addr is an IPv4 /32 CIDR (e.g. "172.16.15.6/32")
// that falls inside the server subnet.
func ValidateAddress(sn ServerNet, addr string) error {
	if !addrRe.MatchString(addr) {
		return fmt.Errorf("clients: address %q must be an IPv4 /32 CIDR (e.g. \"172.16.15.6/32\")", addr)
	}
	ip, _, err := net.ParseCIDR(addr)
	if err != nil {
		return fmt.Errorf("clients: parse address %q: %w", addr, err)
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return fmt.Errorf("clients: address %q is not IPv4", addr)
	}
	if sn.Subnet == nil || !sn.Subnet.Contains(ip4) {
		return fmt.Errorf("clients: address %q is outside the server subnet %s", addr, sn.Subnet)
	}
	return nil
}

// ValidateOverride validates an operator-supplied manual address and returns it
// normalized as "ip/32". It rejects an address that is malformed, out of
// subnet, equal to the reserved server IP, or already in use. used entries may
// be "ip" or "ip/32".
func ValidateOverride(sn ServerNet, addr string, used []string) (string, error) {
	if err := ValidateAddress(sn, addr); err != nil {
		return "", err
	}
	ip, _, err := net.ParseCIDR(addr)
	if err != nil {
		return "", fmt.Errorf("clients: parse address %q: %w", addr, err)
	}
	v, _ := ipToUint32(ip)
	if sv, ok := ipToUint32(sn.ServerIP); ok && v == sv {
		return "", fmt.Errorf("clients: address %q is the reserved server IP", addr)
	}
	for _, u := range used {
		if uv, ok := parseHostUint32(u); ok && uv == v {
			return "", fmt.Errorf("clients: address %q is already in use", addr)
		}
	}
	return uint32ToIP(v).String() + "/32", nil
}
