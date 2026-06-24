// Package geoip resolves peer endpoint IP addresses to a country and city
// using an embedded DB-IP IP-to-City Lite database. The .mmdb is baked into
// the binary at build time via embed.FS so the dashboard needs zero outbound
// network for geolocation and leaks no peer IPs to a third-party API.
//
// DB-IP IP-to-City Lite ships in the MaxMind GeoIP2-compatible MMDB schema,
// so the github.com/oschwald/geoip2-golang reader works unchanged — the
// City record's Country.IsoCode / City.Names / Location fields are identical.
// (It replaced MaxMind GeoLite2, which is no longer freely redistributable
// under CC BY; DB-IP Lite is CC BY 4.0 — attribution only.)
//
// One Service is constructed in main.go and shared across handlers; the
// underlying maxminddb reader is safe for concurrent reads, and lookups are
// microsecond-cheap so no caching layer is layered on top.
package geoip

import (
	_ "embed"
	"fmt"
	"net"

	"github.com/oschwald/geoip2-golang"
)

// dbip-city-lite.mmdb is vendored alongside this file under CC BY 4.0; see
// LICENSE-DB-IP.txt for the required attribution ("IP Geolocation by DB-IP").
// Refreshed by hand per README.md — there is intentionally no auto-update loop.
//
//go:embed dbip-city-lite.mmdb
var mmdbBytes []byte

// Service wraps an in-memory DB-IP IP-to-City Lite reader (GeoIP2-format mmdb).
// Construct with New; the embedded mmdb is decoded on construction and held for
// the lifetime of the process. Safe for concurrent reads.
type Service struct {
	reader *geoip2.Reader
}

// New constructs a Service from the embed.FS-bundled dbip-city-lite.mmdb.
// Returns an error only if the embedded blob fails to decode — which would
// indicate a corrupt commit, not a runtime fault.
func New() (*Service, error) {
	reader, err := geoip2.FromBytes(mmdbBytes)
	if err != nil {
		return nil, fmt.Errorf("geoip: parse embedded mmdb: %w", err)
	}
	return &Service{reader: reader}, nil
}

// Lookup resolves ip to (countryISO, city). Returns ("", "") when:
//   - ip is nil or unspecified (0.0.0.0, ::)
//   - ip is in an RFC1918 / loopback / link-local / IPv6 ULA range
//   - the mmdb has no record for the address
//
// Caller is responsible for never logging the ip itself — geolocation
// is a per-row UI hint, not a metric we want in journald.
func (s *Service) Lookup(ip net.IP) (country, city string) {
	// Cheap stdlib guards first — avoids burning a mmdb lookup on addresses
	// the database has no business resolving anyway. Order matters only for
	// readability; each predicate is independent.
	if ip == nil || ip.IsUnspecified() {
		return "", ""
	}
	if ip.IsLoopback() {
		return "", ""
	}
	if ip.IsPrivate() {
		return "", ""
	}
	if ip.IsLinkLocalUnicast() {
		return "", ""
	}

	record, err := s.reader.City(ip)
	if err != nil || record == nil {
		return "", ""
	}
	// Empty strings are valid mmdb responses (geolocated IP with no country
	// or city mapping) — pass them through rather than fabricating a
	// fallback that the UI would have to special-case anyway.
	return record.Country.IsoCode, record.City.Names["en"]
}

// Close releases the underlying mmdb reader. main.go defers this; any error
// returned at shutdown is informational only.
func (s *Service) Close() error {
	return s.reader.Close()
}
