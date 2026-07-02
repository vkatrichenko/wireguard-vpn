package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
	"wireguard-dashboard/internal/systemd"
	"wireguard-dashboard/internal/wg"
)

// geoPeerJSON mirrors the server.geoPeer JSON contract (the struct is
// unexported). Kept local to the test so a field-tag change in the handler
// surfaces here as a decode mismatch.
type geoPeerJSON struct {
	Name     string  `json:"name"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	City     string  `json:"city"`
	Country  string  `json:"country"`
	Online   bool    `json:"online"`
	LastSeen string  `json:"last_seen"`
}

type geoResponseJSON struct {
	Peers       []geoPeerJSON `json:"peers"`
	NotMappable int           `json:"not_mappable"`
}

// multiPeerClientsfileSvc builds a manifest from name/pubkey pairs so the
// /api/geo join can exercise more than one peer. Mirrors seededClientsfileSvc
// but for an arbitrary list.
func multiPeerClientsfileSvc(t *testing.T, clients []clientsfile.Client) *clientsfile.Service {
	t.Helper()
	body, err := json.Marshal(clients)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return &clientsfile.Service{
		Reader: func(string) ([]byte, error) { return body, nil },
		Path:   "/test/clients.json",
	}
}

// multiPeerWgSvc emits the server-info line plus one `wg show wg0 dump` peer
// line per supplied wg.Peer, using the same 8-field tab layout seededWgSvc
// builds. handshakeAgo positions each peer's latest handshake relative to now.
func multiPeerWgSvc(peers []wg.Peer, handshakeAgo time.Duration) *wg.Service {
	var b strings.Builder
	b.WriteString("server-key\tserver-pub\t51820\toff\n")
	hs := time.Now().Add(-handshakeAgo).Unix()
	for _, p := range peers {
		fmt.Fprintf(&b, "%s\t(none)\t%s\t%s\t%d\t%d\t%d\toff\n",
			p.PublicKey, p.Endpoint, "0.0.0.0/0", hs, p.TransferRx, p.TransferTx)
	}
	out := b.String()
	return &wg.Service{
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(out), nil
		},
		Iface: "wg0",
	}
}

// TestHandleGetGeo_MappableAndExcluded proves the /api/geo contract end-to-end
// with injected fakes:
//
//   - a peer with a public endpoint AND coordinates (resolver OK=true) appears
//     in Peers with its coordinates, online flag, and a non-empty last_seen;
//   - a peer whose resolver returns OK=false (RFC1918 / unresolvable /
//     missing-coords — all collapse to the same not-mappable branch) is
//     EXCLUDED from Peers and counted in not_mappable;
//   - a manifest peer with no live wg endpoint (pending) is also not-mappable.
//
// The resolver is staticGeoResolver, so OK is the single knob deciding
// mappability — the real geoip filtering is covered by the geoip package tests.
// To drive two different OK outcomes from one constant-return resolver we run
// two subtests, each with its own resolver, rather than one resolver that
// branches on IP (which the fake can't do).
func TestHandleGetGeo_MappableAndExcluded(t *testing.T) {
	const (
		fakeIP  = "203.0.113.1"
		fakeKey = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK="

		mappableKey  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
		mappableName = "alice"
		mappableEP   = "198.51.100.42:51820"

		pendingKey  = "CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC="
		pendingName = "dave" // in the manifest but NOT live on wg0
	)

	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: fakeIP},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte(fakeKey + "\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))

	// Manifest has two peers; wg0 only reports the mappable one — `dave` is
	// pending (no endpoint) and must land in not_mappable.
	clients := []clientsfile.Client{
		{Name: mappableName, Address: "172.16.15.5/32", PublicKey: mappableKey},
		{Name: pendingName, Address: "172.16.15.6/32", PublicKey: pendingKey},
	}
	clientsSvc := multiPeerClientsfileSvc(t, clients)
	wgSvc := multiPeerWgSvc([]wg.Peer{
		{PublicKey: mappableKey, Endpoint: mappableEP, TransferRx: 1, TransferTx: 2},
	}, 10*time.Second) // 10s ago → within onlineThreshold → online

	t.Run("resolver OK -> mappable peer plus pending peer not-mappable", func(t *testing.T) {
		geo := staticGeoResolver{country: "US", city: "San Francisco", lat: 37.77, lon: -122.41, ok: true}

		resp := doGeoRequest(t, infoSvc, &systemdSvc, clientsSvc, wgSvc, geo)

		if len(resp.Peers) != 1 {
			t.Fatalf("len(peers) = %d, want 1; resp=%+v", len(resp.Peers), resp)
		}
		p := resp.Peers[0]
		if p.Name != mappableName {
			t.Errorf("peer name = %q, want %q", p.Name, mappableName)
		}
		if p.Lat != 37.77 || p.Lon != -122.41 {
			t.Errorf("coords = (%v,%v), want (37.77,-122.41)", p.Lat, p.Lon)
		}
		if p.City != "San Francisco" || p.Country != "US" {
			t.Errorf("city/country = %q/%q, want San Francisco/US", p.City, p.Country)
		}
		if !p.Online {
			t.Errorf("online = false, want true (handshake 10s ago)")
		}
		if p.LastSeen == "" || p.LastSeen == "never" {
			t.Errorf("last_seen = %q, want a non-empty recent string", p.LastSeen)
		}
		// Exactly one excluded peer: the pending `dave`.
		if resp.NotMappable != 1 {
			t.Errorf("not_mappable = %d, want 1", resp.NotMappable)
		}
		// The excluded peer must never appear in Peers.
		for _, gp := range resp.Peers {
			if gp.Name == pendingName {
				t.Errorf("pending peer %q unexpectedly present in Peers", pendingName)
			}
		}
	})

	t.Run("resolver not-OK -> live peer excluded, both not-mappable", func(t *testing.T) {
		// OK=false simulates an RFC1918 / coordinate-less endpoint: the live
		// peer is now excluded too, so BOTH manifest peers are not-mappable.
		geo := staticGeoResolver{ok: false}

		resp := doGeoRequest(t, infoSvc, &systemdSvc, clientsSvc, wgSvc, geo)

		if len(resp.Peers) != 0 {
			t.Fatalf("len(peers) = %d, want 0; resp=%+v", len(resp.Peers), resp)
		}
		if resp.NotMappable != 2 {
			t.Errorf("not_mappable = %d, want 2", resp.NotMappable)
		}
	})

	t.Run("nil resolver -> every peer not-mappable, no panic", func(t *testing.T) {
		resp := doGeoRequest(t, infoSvc, &systemdSvc, clientsSvc, wgSvc, nil)
		if len(resp.Peers) != 0 {
			t.Fatalf("len(peers) = %d, want 0 with nil resolver", len(resp.Peers))
		}
		if resp.NotMappable != 2 {
			t.Errorf("not_mappable = %d, want 2 with nil resolver", resp.NotMappable)
		}
	})
}

// doGeoRequest wires a server with the given fakes, issues GET /api/geo, and
// decodes the response. Asserts 200, the application/json content type, and a
// non-null Peers slice (the "[]" not "null" contract) before returning. The
// remaining proc/db/disk/processes/netdev deps come from the package's shared
// fakes — /api/geo doesn't touch them, but server.New requires them.
func doGeoRequest(
	t *testing.T,
	infoSvc *serverinfo.Service,
	systemdSvc *systemd.Service,
	clientsSvc *clientsfile.Service,
	wgSvc *wg.Service,
	geo server.GeoResolver,
) geoResponseJSON {
	t.Helper()

	handler, err := server.New(dashboard.WebFS(), infoSvc, systemdSvc, clientsSvc, wgSvc, fakeProcSvc(), newTestDB(t), geo, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(), nil, nil, nil, emptyClientsSvc(t), "local", nil)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/geo", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	// Non-null array contract: an all-excluded fleet must emit "peers":[].
	if !strings.Contains(rec.Body.String(), `"peers":[`) {
		t.Errorf("body missing non-null peers array:\n%s", rec.Body.String())
	}

	var resp geoResponseJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, rec.Body.String())
	}
	return resp
}
