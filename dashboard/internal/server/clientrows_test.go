package server

import (
	"net"
	"testing"
	"time"

	"wireguard-dashboard/internal/geoip"
	"wireguard-dashboard/internal/wg"
)

// fakeGeoResolver returns a fixed country/city for every lookup. The full
// resolver-contract (private-range filtering, mmdb misses, IPv6) lives in
// internal/geoip; buildClientRows only cares that the resolver hook is wired
// through, so a constant-return fake is enough.
type fakeGeoResolver struct {
	country string
	city    string
}

func (f fakeGeoResolver) Lookup(_ net.IP) (string, string) { return f.country, f.city }

// LookupGeo satisfies the GeoResolver interface (extended in spec 006 Slice 3).
// buildClientRows only consults Lookup, so this returns a not-OK GeoPoint —
// the /api/geo handler's resolver behaviour is exercised with staticGeoResolver
// in server_test.go, which carries controllable coordinates.
func (f fakeGeoResolver) LookupGeo(_ net.IP) geoip.GeoPoint { return geoip.GeoPoint{} }

// TestBuildClientRows_Geo proves the resolver is invoked exactly when the
// peer's Endpoint is a valid `host:port` with a parseable IP, and that every
// failure mode (no endpoint, malformed endpoint) leaves the row's Geo at its
// zero value rather than poisoning the join.
func TestBuildClientRows_Geo(t *testing.T) {
	geo := fakeGeoResolver{country: "US", city: "Mountain View"}
	now := time.Unix(1_700_000_000, 0).UTC()

	tests := []struct {
		name     string
		endpoint string
		wantGeo  Geo
	}{
		{
			name:     "resolves valid ipv4 endpoint",
			endpoint: "8.8.8.8:51820",
			wantGeo:  Geo{Country: "US", City: "Mountain View"},
		},
		{
			name:     "empty endpoint yields empty geo",
			endpoint: "",
			wantGeo:  Geo{},
		},
		{
			name:     "malformed endpoint yields empty geo",
			endpoint: "garbage",
			wantGeo:  Geo{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			peers := []wg.Peer{{
				PublicKey:       "peer-pub-key",
				Endpoint:        tc.endpoint,
				LatestHandshake: now.Add(-30 * time.Second),
			}}
			rows := buildClientRows(nil, peers, now, geo)
			if len(rows) != 1 {
				t.Fatalf("len(rows) = %d, want 1", len(rows))
			}
			if rows[0].Geo != tc.wantGeo {
				t.Errorf("Geo = %+v, want %+v", rows[0].Geo, tc.wantGeo)
			}
		})
	}
}

// TestBuildClientRows_NilGeoResolver confirms passing nil is a valid no-op —
// every row's Geo stays at its zero value. The other consumers of
// buildClientRows pass nil from tests, so a regression here would cascade.
func TestBuildClientRows_NilGeoResolver(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	peers := []wg.Peer{{
		PublicKey:       "peer-pub-key",
		Endpoint:        "8.8.8.8:51820",
		LatestHandshake: now.Add(-30 * time.Second),
	}}
	rows := buildClientRows(nil, peers, now, nil)
	if len(rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(rows))
	}
	if rows[0].Geo != (Geo{}) {
		t.Errorf("Geo = %+v, want zero value with nil resolver", rows[0].Geo)
	}
}
