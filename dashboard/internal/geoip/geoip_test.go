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

// TestServiceLookupGeo proves LookupGeo surfaces coordinates for public IPs and
// reports OK=false for every guarded / unresolvable input. Coordinates are NOT
// pinned to exact values — DB-IP drifts lat/lon between monthly releases (same
// reason the country-only assertions above skip city). Instead we assert OK,
// a sane country code, and that the coordinates land in a broad bounding box
// (correct hemisphere + plausible magnitude). The resolved point is logged in
// -v output so a maintainer can eyeball drift.
func TestServiceLookupGeo(t *testing.T) {
	svc, err := New()
	if err != nil {
		t.Fatalf("geoip.New: %v", err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Logf("svc.Close: %v", err)
		}
	})

	t.Run("us-public-ip-resolves-with-coords", func(t *testing.T) {
		gp := svc.LookupGeo(net.ParseIP("8.8.8.8"))
		if !gp.OK {
			t.Fatalf("OK = false, want true for public IP; got %+v", gp)
		}
		if gp.Country != "US" {
			t.Errorf("country = %q, want US", gp.Country)
		}
		// Continental US: lat ~25..50 N, lon ~-125..-65. A broad box tolerant
		// to monthly DB drift while still proving real coordinates landed.
		if gp.Lat <= 0 || gp.Lat < 20 || gp.Lat > 55 {
			t.Errorf("lat = %v, want northern-hemisphere US range (20..55)", gp.Lat)
		}
		if gp.Lon > -60 || gp.Lon < -130 {
			t.Errorf("lon = %v, want continental-US range (-130..-60)", gp.Lon)
		}
		t.Logf("resolved 8.8.8.8 -> %+v", gp)
	})

	t.Run("eu-public-ip-resolves-with-coords", func(t *testing.T) {
		gp := svc.LookupGeo(net.ParseIP("193.0.14.129"))
		if !gp.OK {
			t.Fatalf("OK = false, want true for public IP; got %+v", gp)
		}
		if gp.Country != "NL" {
			t.Errorf("country = %q, want NL", gp.Country)
		}
		// Netherlands: lat ~50..54 N, lon ~3..7 E. Broad box for drift.
		if gp.Lat < 45 || gp.Lat > 56 {
			t.Errorf("lat = %v, want NL range (45..56)", gp.Lat)
		}
		if gp.Lon < 0 || gp.Lon > 10 {
			t.Errorf("lon = %v, want NL range (0..10)", gp.Lon)
		}
		t.Logf("resolved 193.0.14.129 -> %+v", gp)
	})

	notOK := []struct {
		name string
		ip   net.IP
	}{
		{"rfc1918-10x", net.ParseIP("10.0.0.1")},
		{"rfc1918-192-168-x", net.ParseIP("192.168.1.1")},
		{"ipv6-link-local", net.ParseIP("fe80::1")},
		{"ipv4-loopback", net.ParseIP("127.0.0.1")},
		{"ipv4-unspecified", net.ParseIP("0.0.0.0")},
		{"nil-ip", net.ParseIP("not-an-ip")},
	}
	for _, tc := range notOK {
		t.Run(tc.name, func(t *testing.T) {
			gp := svc.LookupGeo(tc.ip)
			if gp.OK {
				t.Errorf("OK = true, want false for guarded input; got %+v", gp)
			}
			if gp != (GeoPoint{}) {
				t.Errorf("not-OK GeoPoint = %+v, want zero value", gp)
			}
		})
	}
}
