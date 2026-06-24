# Technical Specification: Connection History & Geo Map

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

A **pure dashboard-application change** built on data already collected — no Terraform, IAM, or network changes. Two additions:

1. **Connection history** — derive per-client online/offline state, last-seen, and inferred sessions from the handshake samples the poller already writes to SQLite (`/var/lib/wireguard-dashboard/metrics.db`). Surface a timeline in the existing Clients expand panel.
2. **Geo map** — extend the existing GeoLite2 lookup (`internal/geoip`) to return latitude/longitude, and render an **offline, self-contained** world map with peers projected onto it.

Both reuse the existing poller, SQLite store, htmx refresh, time-range selector, and Chart.js/asset-vendoring conventions. The hard constraint is **zero outbound requests**: the map base layer is an embedded asset, not external tiles.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Data Model / Storage

- **Reuse, don't add unbounded tables.** The poller already persists per-peer handshake/transfer samples. Connection history is **derived** from those samples at query time (or via a lightweight materialized view), not stored as a separate ever-growing event log.
- If a dedicated handshake-event table is warranted for efficient timeline queries, it must inherit the **existing retention/expiry** (the ~8-day window from 003 §2.8 if present) — no new unbounded growth (functional spec §2.2).
- **Session inference** (no schema needed beyond samples): order a peer's handshakes by time; a gap > `sessionGapThreshold` (proposed 10 min, `[NEEDS CLARIFICATION]`) closes the prior session and opens a new one. "Connected time" = sum of session spans within the selected range; "online now" = latest handshake within `onlineThreshold` (3 min, matching 003).

### API Contracts

- **`GET /api/clients/{name}/history?range={1h|6h|24h|7d}`** — returns the client's timeline for the range: ordered session spans (start/end), total connected time, session count, online/last-seen. JSON; read-only.
- **`GET /api/geo`** (or `/api/clients/geo`) — returns the mappable peers: `{name, lat, lon, city, country, online, lastSeen}` for peers with a resolvable public endpoint, plus a count of non-mappable peers. RFC1918/unresolvable excluded.
- Both follow the existing `/api/...` JSON conventions; corresponding `/partial/...` endpoints render the htmx fragments (timeline panel, map card) consistent with 003.

### Component Breakdown

| Component | Path | Responsibility |
|-----------|------|----------------|
| `geoip` (extend) | `dashboard/internal/geoip/` | Add lat/lon to the existing City lookup result (the GeoLite2-City db already carries `location.latitude/longitude`). |
| History derivation | `dashboard/internal/db/` (or a new `internal/history/`) | Pure functions over retained samples → sessions, connected-time, last-seen. Table-testable with synthetic sample sets. |
| Map projection | client-side JS in `dashboard/web/static/` | Equirectangular projection: `x = (lon+180)/360 * W`, `y = (90-lat)/180 * H`, markers absolutely positioned over the embedded base map. No projection library needed. |
| World base map | `dashboard/web/static/` (embedded) | A vendored, self-contained **SVG world map** (public-domain/permissively-licensed outline). Embedded via the existing `embed.FS`; served from `/static/`. No external tiles. |
| Clients timeline panel | `dashboard/web/templates/.../clients` (extend 003 expand panel) | Render online/offline bands + summary. Can reuse Chart.js (e.g. a horizontal range/bar) to avoid a new dependency. |
| Geo map card | `dashboard/web/templates/cards/` (new) | The map + legend + "N not mappable" caption + per-marker hover/tap detail. |

### Logic / Algorithm — offline map

- **Why not Leaflet/Mapbox:** they require online tiles or a CDN, which violates the tunnel-only, zero-outbound constraint and the existing vendored-asset model. An embedded SVG outline + computed marker positions is fully offline and adds no heavyweight dependency.
- **Online vs. offline encoding** via marker color/opacity; **co-located peers** get a count badge (proposed) rather than a clustering library (`[NEEDS CLARIFICATION]`).
- The projection math is trivial and lives in a small static JS module alongside the existing theme/chart scripts; chart/marker colors honor the dark-mode theme (003 §2.9).

---

## 3. Impact and Risk Analysis

- **System Dependencies:** the SQLite store and poller (existing), `internal/geoip` + embedded GeoLite2-City (existing), the htmx refresh + time-range selector (003). No AWS, no network, no IAM changes.
- **Potential Risks & Mitigations:**
  - **Query cost of deriving sessions on every 10s tick.** *Mitigation:* index handshake samples by `(peer, ts)`; cap the scan to the selected range; if needed, cache derived sessions between ticks. Keep derivation pure and benchmarked.
  - **Retention growth.** *Mitigation:* derive from existing retained samples; if a handshake-event table is added, attach it to the existing expiry job (functional spec §2.2) — no unbounded table.
  - **GeoLite2 lat/lon accuracy / missing coordinates.** *Mitigation:* treat missing coordinates like unresolvable endpoints — exclude from the map and count them in the "not mappable" caption.
  - **Map asset size / licensing.** *Mitigation:* choose a lightweight, permissively-licensed SVG world outline; record its source/license (the repo is going open-source). Keep it embedded so there's no runtime fetch.
  - **Marker overlap at city granularity.** *Mitigation:* badge co-located peers; revisit clustering only if real usage shows crowding.

---

## 4. Testing Strategy

- **Unit (core):** table-driven tests for session inference and last-seen — feed synthetic handshake-sample sequences (continuous, gappy, single, none) and assert session count, connected-time, online/offline, and "last seen" rendering, including the gap-threshold boundary.
- **Unit:** projection function tests — known lat/lon → expected x/y within tolerance (corners, equator/prime-meridian, negative coords).
- **Handler tests:** `httptest` for `/api/clients/{name}/history` (each range, unknown client → 404, empty history) and `/api/geo` (mappable vs. excluded peers, the not-mappable count) with injected fakes for the store and geoip.
- **Visual/manual:** confirm the map renders offline (load with no network), markers land in plausible locations, dark-mode colors apply, and the timeline is legible on mobile.
- **Quality gate:** `make test` in `dashboard/` before any "done" claim.
