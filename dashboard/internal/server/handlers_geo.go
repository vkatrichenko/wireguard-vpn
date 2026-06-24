package server

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"wireguard-dashboard/internal/geoip"
	"wireguard-dashboard/internal/wg"
)

// geoPeer is one mappable peer in the GET /api/geo response: a name, resolved
// coordinates + country/city, and the same online / last-seen pair the Clients
// tab shows. Only peers with BOTH a resolvable public endpoint AND coordinates
// appear here — see handleGetGeo for the exclusion rules.
type geoPeer struct {
	Name     string  `json:"name"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
	City     string  `json:"city,omitempty"`
	Country  string  `json:"country,omitempty"`
	Online   bool    `json:"online"`
	LastSeen string  `json:"last_seen"`
}

// geoResponse is the payload of GET /api/geo. Peers is always a non-nil slice
// so the encoder emits "[]" not "null" for an all-excluded fleet (matching the
// /api/clients + history contracts). NotMappable counts the peers excluded for
// any reason — no endpoint, RFC1918 / unresolvable endpoint, or a resolvable
// endpoint with no coordinates — so the UI can render the "N not mappable"
// caption without re-deriving it.
type geoResponse struct {
	Peers       []geoPeer `json:"peers"`
	NotMappable int       `json:"not_mappable"`
}

// handleGetGeo returns the mappable-peer snapshot the geo map (spec 006 Slice 4)
// will plot. It reuses the same manifest + `wg show` join as handleGetClients,
// then for each manifest peer resolves its live endpoint IP through the geoip
// resolver. A peer is mappable iff it has a live endpoint that parses to a
// public IP AND the mmdb returns coordinates; everything else (pending peers
// with no endpoint, RFC1918 endpoints, unresolvable / coordinate-less records)
// is counted in NotMappable and dropped from Peers.
//
// online is derived from the peer's LIVE LatestHandshake against onlineThreshold
// — the same rule buildClientRows uses — rather than from a per-peer
// handshake_events history query. This is a deliberate reuse of the live
// indicator: the map refreshes on the same 10s tick as the client list, and an
// N-query-per-tick DB hit (one history scan per peer) would serialise behind
// the single SQLite connection for no UI gain. last_seen is the humanAgo form
// of that same live handshake.
//
// A nil geoip resolver (geo is advisory; many tests pass nil) makes every peer
// not-mappable — never a panic. Manifest peers present on wg0 but with no
// endpoint yet, and "unknown" wg peers absent from the manifest, are likewise
// excluded: the map is keyed on named, located peers only.
//
// Either underlying fetch failing produces a 500 with the error in the body,
// matching the sibling /api/clients handler — without the join there's nothing
// mappable to return.
func (s *server) handleGetGeo(w http.ResponseWriter, r *http.Request) {
	clients, err := s.clientsfileSvc.Load(r.Context())
	if err != nil {
		slog.Error("GET /api/geo: clientsfile load failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	peers, err := s.wgSvc.Show(r.Context())
	if err != nil {
		slog.Error("GET /api/geo: wg show failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	peerByKey := make(map[string]wg.Peer, len(peers))
	for _, p := range peers {
		peerByKey[p.PublicKey] = p
	}

	now := time.Now()
	resp := geoResponse{Peers: make([]geoPeer, 0, len(clients))}

	for _, c := range clients {
		p, live := peerByKey[c.PublicKey]
		if !live {
			resp.NotMappable++
			continue
		}
		gp := resolveGeoPoint(s.geoipSvc, p.Endpoint)
		if !gp.OK {
			resp.NotMappable++
			continue
		}
		online := !p.LatestHandshake.IsZero() && now.Sub(p.LatestHandshake) <= onlineThreshold
		resp.Peers = append(resp.Peers, geoPeer{
			Name:     c.Name,
			Lat:      gp.Lat,
			Lon:      gp.Lon,
			City:     gp.City,
			Country:  gp.Country,
			Online:   online,
			LastSeen: humanAgo(p.LatestHandshake),
		})
	}

	body, err := json.Marshal(resp)
	if err != nil {
		slog.Error("GET /api/geo: json marshal failed", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

// resolveGeoPoint mirrors resolveGeo's endpoint parsing (host:port → net.IP)
// but returns the full GeoPoint (with coordinates + OK) instead of the
// country/city-only Geo. A nil resolver, empty/malformed endpoint, or
// unparseable host all yield a not-OK GeoPoint — the caller treats that as
// "not mappable".
func resolveGeoPoint(geo GeoResolver, endpoint string) geoip.GeoPoint {
	if geo == nil || endpoint == "" {
		return geoip.GeoPoint{}
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return geoip.GeoPoint{}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return geoip.GeoPoint{}
	}
	return geo.LookupGeo(ip)
}
