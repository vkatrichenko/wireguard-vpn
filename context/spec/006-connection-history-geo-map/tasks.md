# Tasks: Connection History & Geo Map

- **Functional Spec:** [functional-spec.md](./functional-spec.md)
- **Technical Spec:** [technical-considerations.md](./technical-considerations.md)
- **Stack:** Go std-lib HTTP + `html/template`/htmx + Chart.js (dashboard `dashboard/`). Pure-app change — no Terraform/IAM/network. All sub-tasks → `go-fullstack`.

**Confirmed decisions** (resolved `[NEEDS CLARIFICATION]`): session-gap threshold **10 min**; co-located map peers → **count badge** (no clustering lib); geo map → **card on the Clients tab**.

**Grounding (existing schema/seams):** handshake history derives from the existing `handshake_events(ts, public_key, name)` table (indexed on `ts`, already retention-swept — **no new table**, satisfies §2.2). `internal/geoip.Lookup(ip) → (country, city)` is extended to also return lat/lon (the GeoLite2-City record already carries `Location.Latitude/Longitude`). Reuse the 003 expand panel, time-range selector, 10s htmx tick, Chart.js, dark-mode theming, and `embed.FS` asset vendoring. Hard constraint: **zero outbound requests** (offline map).

Each slice leaves the dashboard runnable; in-session proof is `go test` + httptest + curl against injected fakes. Map rendering + offline guarantee need a manual visual check (no browser MCP), flagged in the table.

---

- [ ] **Slice 1: Per-client online / last-seen / connection summary (data + expand-panel text)**
  - [ ] Add a pure session-derivation package `internal/history/` (or funcs in `internal/db/`): given a peer's `handshake_events` ordered by `ts`, infer sessions (gap > **10 min** opens a new session), compute connected-time and session-count for a range, `online` = latest handshake within **3 min** (matching 003), and a human "last seen" string ("2 days ago" / "never"). No I/O — operates on passed-in samples. **[Agent: go-fullstack]**
  - [ ] Unit-test the derivation: synthetic handshake sequences (continuous, gappy, single, none) asserting session count, connected-time, online/offline, last-seen rendering, and the **10-min gap boundary** (exactly-10 vs just-over). **[Agent: go-fullstack]**
  - [ ] Add a `db` query returning a peer's `handshake_events` within a range (reuse the `idx_handshake_events_ts` index; filter by `public_key` + `ts BETWEEN`). **[Agent: go-fullstack]**
  - [ ] Add `GET /api/clients/{name}/history?range={1h|6h|24h|7d}` (default 24h) → JSON: ordered session spans, total connected time, session count, online, lastSeen. `404` unknown client; empty history returns an empty timeline, not an error. Handler tests (`httptest` + fake store): each range, unknown → 404, empty history. **[Agent: go-fullstack]**
  - [ ] Render the summary (online/last-seen + connected-time + session-count) in the **Clients expand panel** (extend the 003 detail panel), honoring the existing range selector. **[Agent: go-fullstack]**
  - [ ] **Verify:** `go test ./...` in `dashboard/` green; `gofmt`/`go vet` clean; httptest covers the API (the curl-equivalent path); expand-panel render test asserts the summary fields. Confirm no new table/migration was added (derivation is query-time over `handshake_events`). **[Agent: go-fullstack]**

- [ ] **Slice 2: Connection timeline visualization (online/offline bands) in the expand panel**
  - [ ] Render the session spans from Slice 1 as a **timeline** in the expand panel over the selected range — online/offline bands (reuse Chart.js, e.g. a horizontal floating-bar, to avoid a new dependency); colors honor the dark-mode theme (003 §2.9). Fresh-host / insufficient-history shows whatever exists with no error. **[Agent: go-fullstack]**
  - [ ] Wire the timeline into the existing 10s htmx refresh and the 1h/6h/24h/7d range selector; a failed refresh keeps last values + the global "Stale data" indicator (003 §2.11). **[Agent: go-fullstack]**
  - [ ] **Verify:** render/partial test asserts the timeline canvas + data-range attribute present and bands reflect injected sessions; `go test` green. Visual legibility (incl. mobile) is the manual check below. **[Agent: go-fullstack]**

- [ ] **Slice 3: GeoLite2 lat/lon extension + `/api/geo` (map data, no UI yet)**
  - [ ] Extend `internal/geoip` to also surface `Location.Latitude/Longitude` (e.g. a `LookupGeo(ip) → {country, city, lat, lon, ok}`), treating missing coordinates like an unresolvable lookup. Keep the existing `Lookup` signature working for 003 callers. **[Agent: go-fullstack]**
  - [ ] Add `GET /api/geo` → JSON: mappable peers `{name, lat, lon, city, country, online, lastSeen}` (reuse Slice-1 online/last-seen) for peers with a resolvable public endpoint + coords; **exclude** RFC1918 / unresolvable / missing-coord peers and return a `notMappable` count. **[Agent: go-fullstack]**
  - [ ] Unit-test geoip lat/lon (known IP → expected coords; private/unresolvable → not-ok); handler test for `/api/geo` (mappable vs excluded, `notMappable` count) with injected geoip + clients/wg fakes. **[Agent: go-fullstack]**
  - [ ] **Verify:** `go test ./...` green; `gofmt`/`go vet` clean; assert excluded peers never appear in the payload and the count is correct. **[Agent: go-fullstack]**

- [ ] **Slice 4: Offline world-map card on the Clients tab (embedded SVG + projected markers)**
  - [ ] Vendor a permissively-licensed **SVG world outline** into `dashboard/web/static/` (embedded via `embed.FS`, served from `/static/`); record its source + license in-repo (repo is going open-source). No external tiles/fonts/scripts. **[Agent: go-fullstack]**
  - [ ] Add a small static JS module (alongside the existing theme/chart scripts) doing the **equirectangular projection** `x=(lon+180)/360*W`, `y=(90-lat)/180*H` and absolutely positioning markers over the SVG; pull data from `/api/geo` on the 10s tick. Co-located peers → **one marker + count badge**; hover/tap shows name, city/country, online/last-seen. Online vs offline encoded by color/opacity with a **legend**; marker colors honor dark mode. **[Agent: go-fullstack]**
  - [ ] Add the **map card to the Clients tab** template with the legend, a "**N not mappable**" caption, and the empty state ("No mappable peers."). Mobile per 003 §2.10 (scales to viewport width, no horizontal scroll ≥360px, touch targets ≥44px). **[Agent: go-fullstack]**
  - [ ] Unit-test the projection function (known lat/lon → expected x/y within tolerance: corners, equator/prime-meridian, negative coords). **[Agent: go-fullstack]**
  - [ ] **Verify (in-session):** `go test ./...` green; `gofmt`/`go vet` clean; partial/render test asserts the map card, legend, and not-mappable caption render; **grep templates + `web/static/` for any external URL** (`http(s)://`, CDN, font host) to prove zero outbound references. **[Agent: go-fullstack]**

- [ ] **Slice 5: End-to-end manual verification on the deployed dashboard (operator-assisted)**
  - [ ] On a VPN client, load the Clients tab: confirm the **timeline** renders per client (online/offline bands, connected-time, session count) across ranges; the **map** renders **with no network** (offline — check devtools shows no outbound requests), markers land in plausible locations, online/offline encoding + legend read correctly, co-located peers show a count badge, the "N not mappable" caption is right, and both views are legible on a phone. Dark-mode colors apply. **[Manual]**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 4 SVG asset | Need a permissively-licensed world-outline SVG | Pick a public-domain / CC0 outline (e.g. a simple Natural Earth-derived SVG); record source+license; keep it embedded (no runtime fetch) |
| Slices 2 & 4 visual | No browser MCP for automated UI/chart/map checks | Render/partial tests + projection unit tests cover logic; the map/timeline visual + offline-network proof is the manual Slice 5 |
| Slice 5 | Needs a real VPN client + deployed binary | Owner runs it; treat as the required manual end-to-end (offline-render proof can't be asserted from a green build) |
