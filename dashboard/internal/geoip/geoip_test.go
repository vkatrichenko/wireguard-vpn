// Tests for the geoip Service. These run against the real embedded
// dbip-city-lite.mmdb — the database ships with the binary, so a unit test
// that constructs a Service via geoip.New() is the closest thing to a true
// integration test we can run without network access.
//
// The public-IP assertions only pin country codes, never cities: DB-IP
// rotates city mappings between monthly releases, and a city assertion would
// turn the suite into a recurring maintenance burden for no real benefit. The
// resolved city is surfaced via t.Logf so it remains visible in -v output.

package geoip

import (
	"net"
	"testing"
)

func TestServiceLookup(t *testing.T) {
	svc, err := New()
	if err != nil {
		// A failure here means the embedded mmdb is corrupt — that's a
		// real bug worth surfacing loudly rather than skipping past.
		t.Fatalf("geoip.New: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("svc.Close: %v", err)
		}
	})

	cases := []struct {
		name        string
		ip          net.IP
		wantCountry string
	}{
		{
			name:        "us-public-ip",
			ip:          net.ParseIP("8.8.8.8"),
			wantCountry: "US",
		},
		{
			name:        "eu-public-ip-ripe",
			ip:          net.ParseIP("193.0.14.129"),
			wantCountry: "NL",
		},
		{
			name:        "rfc1918-10x",
			ip:          net.ParseIP("10.0.0.1"),
			wantCountry: "",
		},
		{
			name:        "rfc1918-192-168-x",
			ip:          net.ParseIP("192.168.1.1"),
			wantCountry: "",
		},
		{
			name:        "ipv6-link-local",
			ip:          net.ParseIP("fe80::1"),
			wantCountry: "",
		},
		{
			// net.ParseIP returns nil for unparseable input; this case
			// exercises the nil guard at the top of Lookup.
			name:        "malformed-input",
			ip:          net.ParseIP("not-an-ip"),
			wantCountry: "",
		},
		{
			name:        "ipv4-loopback",
			ip:          net.ParseIP("127.0.0.1"),
			wantCountry: "",
		},
		{
			name:        "ipv4-unspecified",
			ip:          net.ParseIP("0.0.0.0"),
			wantCountry: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			country, city := svc.Lookup(tc.ip)
			if country != tc.wantCountry {
				t.Errorf("country = %q, want %q (city=%q)", country, tc.wantCountry, city)
			}
			if tc.wantCountry == "" && city != "" {
				t.Errorf("expected empty city for guarded input, got %q", city)
			}
			if tc.wantCountry != "" {
				t.Logf("resolved %s -> country=%q city=%q", tc.ip, country, city)
			}
		})
	}
}
