package server

import (
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
func buildClientRows(clients []clientsfile.Client, peers []wg.Peer, now time.Time) []ClientRow {
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
		})
	}

	return rows
}
