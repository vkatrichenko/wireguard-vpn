package server

import (
	"net"
	"time"

	"wireguard-dashboard/internal/clientsfile"
	"wireguard-dashboard/internal/wg"
)

// onlineThreshold is the "last successful handshake" age below which a peer
// is reported as online. Matches the functional spec (§2.1): a client whose
// most recent handshake landed within the last three minutes is shown as
// Online; older or never-handshaked peers fall back to offline.
//
// 3 minutes corresponds to the WireGuard rekey timeout (REKEY_TIMEOUT,
// implicitly upper-bounded around 180s by the protocol's keepalive +
// REJECT_AFTER_TIME constants), so it's a meaningful boundary rather than an
// arbitrary UI choice.
const onlineThreshold = 3 * time.Minute

// Geo is the resolved country/city pair for a peer's most recent endpoint IP.
// Empty strings mean "unresolvable" (private/loopback range, mmdb miss, or no
// endpoint at all) — the template renders them as an em-dash. Both fields
// carry `omitempty` so a fully-empty Geo serialises to `{}` rather than two
// empty strings; the outer field tag's `omitempty` on a struct value does NOT
// drop the empty object itself (encoding/json only omits empty pointers /
// nil interfaces / zero scalars), so a marshalled ClientRow with no geo
// shows `"geo":{}`. Documented here so a reader doesn't mistake it for a bug.
type Geo struct {
	Country string `json:"country,omitempty"` // ISO-3166 alpha-2, e.g. "US"
	City    string `json:"city,omitempty"`    // English city name
}

// GeoResolver looks up an IP. *geoip.Service satisfies this; tests can pass
// a fake. Returning ("", "") is a valid no-op response, not an error — the
// resolver is purely advisory.
type GeoResolver interface {
	Lookup(ip net.IP) (country, city string)
}

// ClientRow is the joined view-model that GET /api/clients returns and that
// cards/client-list.html consumes. It pairs human-friendly manifest fields
// (Name, Address) with live `wg show` state (handshake, byte counters,
// endpoint) into a single row per peer.
//
// Status carries the four states the join can produce — see buildClientRows
// for the full semantics.
type ClientRow struct {
	Name            string    `json:"name"`                      // from manifest, "" if unknown
	Address         string    `json:"address,omitempty"`         // from manifest, "" if unknown
	PublicKey       string    `json:"public_key"`                //
	Status          string    `json:"status"`                    // "online" | "offline" | "pending" | "unknown"
	LatestHandshake time.Time `json:"latest_handshake,omitzero"` // zero if never
	TransferRx      int64     `json:"transfer_rx"`
	TransferTx      int64     `json:"transfer_tx"`
	Endpoint        string    `json:"endpoint,omitempty"`
	Geo             Geo       `json:"geo,omitempty"` // see Geo's doc — `omitempty` on a struct value does not drop the empty object
}

// buildClientRows performs the manifest+wg outer-join and returns rows in
// stable order: manifest order first (status: online | offline | pending),
// followed by any peers present on wg0 but not in the manifest (status:
// unknown). The current time is injected so tests can pin "now".
//
// Status semantics:
//
//   - online — peer is in `wg show` AND latest handshake is within
//     onlineThreshold of `now`.
//   - offline — peer is in `wg show` AND latest handshake is older than
//     onlineThreshold OR has never handshaked.
//   - pending — peer is in the manifest but NOT in `wg show` (e.g. just
//     added; cloud-init hasn't re-run, or the unit was restarted but the
//     peer hasn't connected yet).
//   - unknown — peer is in `wg show` but NOT in the manifest (a manual
//     `wg set ... peer ...` add, or a stale peer left over after a removal).
//
// Ordering is "manifest first, unknowns last" so an operator scanning the
// list sees their named clients in the order Terraform configured them, with
// any out-of-band peers grouped at the bottom for triage.
//
// The returned slice is never nil, even when both inputs are empty — callers
// that JSON-marshal it get a stable `[]` rather than `null`.
//
// geo is optional — pass nil to skip the lookup. The resolver is consulted
// only when the row has a non-empty endpoint and the endpoint parses as
// host:port with a recognisable IP. All failure paths leave Geo as its
// zero value, never blocking the row.
func buildClientRows(clients []clientsfile.Client, peers []wg.Peer, now time.Time, geo GeoResolver) []ClientRow {
	// Inline index — only one caller; not worth a helper in the wg package.
	peerByKey := make(map[string]wg.Peer, len(peers))
	for _, p := range peers {
		peerByKey[p.PublicKey] = p
	}

	// Pre-allocate to len(clients)+len(peers) — the worst case is all
	// manifest entries pending plus all live peers unknown (no overlap),
	// which would never happen in practice, but the capacity hint is cheap.
	rows := make([]ClientRow, 0, len(clients)+len(peers))
	seen := make(map[string]struct{}, len(clients))

	for _, c := range clients {
		seen[c.PublicKey] = struct{}{}
		row := ClientRow{
			Name:      c.Name,
			Address:   c.Address,
			PublicKey: c.PublicKey,
		}
		if p, ok := peerByKey[c.PublicKey]; ok {
			row.LatestHandshake = p.LatestHandshake
			row.TransferRx = p.TransferRx
			row.TransferTx = p.TransferTx
			row.Endpoint = p.Endpoint
			row.Geo = resolveGeo(geo, row.Endpoint)
			if !p.LatestHandshake.IsZero() && now.Sub(p.LatestHandshake) <= onlineThreshold {
				row.Status = "online"
			} else {
				row.Status = "offline"
			}
		} else {
			row.Status = "pending"
		}
		rows = append(rows, row)
	}

	for _, p := range peers {
		if _, ok := seen[p.PublicKey]; ok {
			continue
		}
		rows = append(rows, ClientRow{
			PublicKey:       p.PublicKey,
			Status:          "unknown",
			LatestHandshake: p.LatestHandshake,
			TransferRx:      p.TransferRx,
			TransferTx:      p.TransferTx,
			Endpoint:        p.Endpoint,
			Geo:             resolveGeo(geo, p.Endpoint),
		})
	}

	return rows
}

// resolveGeo runs the resolver against a `host:port` endpoint string and
// returns the resulting Geo. Empty/malformed/unresolvable inputs yield the
// zero Geo. The nil-resolver guard lets callers (and tests) opt out cheaply.
func resolveGeo(geo GeoResolver, endpoint string) Geo {
	if geo == nil || endpoint == "" {
		return Geo{}
	}
	host, _, err := net.SplitHostPort(endpoint)
	if err != nil {
		return Geo{}
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return Geo{}
	}
	country, city := geo.Lookup(ip)
	return Geo{Country: country, City: city}
}
