# Functional Specification: Connection History & Geo Map

- **Roadmap Item:** Not yet on the roadmap (observability follow-on to 003-dashboard-admin-views)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 003 added a per-client view with last-handshake and country/city geolocation, and an Events tab showing the most recent 50 handshakes. Two questions still aren't answerable from the dashboard:

- **"When was this client last actually connected, and how often?"** The Clients tab shows the *latest* handshake, but there's no history — no sense of whether a peer is connected all day or pops on for five minutes, and no at-a-glance "last seen 2 days ago" for peers that are currently offline.
- **"Where, visually, are my peers?"** Country + city text exists per row, but there's no map. For a solo-operator VPN, a map is the fastest way to sanity-check "are all my connections coming from where I expect?"

This spec adds two read-only views built entirely on data the dashboard already has: handshake samples already flow through the poller into SQLite, and the embedded GeoLite2-City database already yields latitude/longitude. It keeps every constraint from 002/003: read-only, VPN-only, no auth, mobile-friendly, single Go binary, **fully offline** (no external tiles or CDNs).

**Success looks like:** the operator can see each client's recent connection timeline and last-seen-when-offline, and can glance at a world map to confirm where peers are connecting from — without SSH and without the dashboard making a single outbound request.

---

## 2. Functional Requirements (The "What")

### 2.1 Per-client connection history

- **As the operator, I want to** see a client's recent connection timeline, **so that I can** tell habitual peers from one-offs and spot a peer that dropped.
  - **Acceptance Criteria:**
    - [ ] Each client has a connection history derived from handshake samples over the dashboard's existing retention window.
    - [ ] A client is shown **online** when its latest handshake is within a freshness threshold (default: 3 minutes) and **offline** otherwise — consistent with the indicator already used in 003 §2.3.
    - [ ] For offline clients, a human-readable **last-seen** is shown (e.g. "last seen 2 days ago", or "never" if no handshake was ever recorded).
    - [ ] Expanding a client (reusing the 003 expand panel) shows a **connection timeline** over the selected time range: online/offline bands, plus a summary of total connected time and session count for the range.
    - [ ] A "session" is **inferred** from consecutive handshakes (WireGuard has no explicit connect/disconnect): a gap larger than the session-gap threshold (**10 minutes**) starts a new session.
    - [ ] When insufficient history exists (fresh host), the timeline shows whatever exists without error.

### 2.2 Events / history retention

- **As the operator, I want** enough history to see patterns, **so that** the timeline isn't limited to the last few minutes.
  - **Acceptance Criteria:**
    - [ ] Connection history honors the dashboard's existing SQLite retention (the 7-day window introduced in 003 §2.8 if present; otherwise the current retention).
    - [ ] The history view uses the **same time-range selector** (1h / 6h / 24h / 7d) where applicable, defaulting to 24h.
    - [ ] No new unbounded growth: history derives from already-retained samples; it does not introduce a table that grows without an expiry.

### 2.3 Geo map of peers

- **As the operator, I want** a world map of where peers connect from, **so that I can** visually confirm expected geography at a glance.
  - **Acceptance Criteria:**
    - [ ] A world map renders markers for peers with a resolvable public endpoint, positioned by GeoLite2 latitude/longitude.
    - [ ] **Online** peers are visually distinct from peers seen recently but currently offline (e.g. color/opacity); the legend explains the encoding.
    - [ ] Hovering/tapping a marker shows the client name, city/country, and online/last-seen status. Overlapping markers at the same location are **stacked into one marker with a count badge** (no clustering library); hover/tap lists the co-located peers.
    - [ ] Peers with RFC1918 / unresolvable endpoints are **excluded** from the map (consistent with the "—" geolocation rule in 003 §2.3) and noted in a small "N not mappable" caption.
    - [ ] The map is **fully offline**: the base map is an embedded/vendored asset; the dashboard makes **no** outbound request and loads no external tiles, fonts, or scripts.
    - [ ] Empty state when no peers are mappable: a neutral map with "No mappable peers."

### 2.4 Placement & navigation

- **As the operator, I want** these to fit the existing layout, **so that** the dashboard stays coherent.
  - **Acceptance Criteria:**
    - [ ] The connection timeline lives in the **Clients** tab expand panel (extending 003 §2.3), not a new tab.
    - [ ] The geo map lives as a **card on the Clients tab** (peer geography), keeping all peer/geography data together.
    - [ ] Tab/range state continues to persist in the URL fragment as in 003 §2.1 / §2.8.

### 2.5 Refresh & mobile

- **As the operator, I want** the new views to behave like the rest, **so that** there are no surprises.
  - **Acceptance Criteria:**
    - [ ] Both views refresh on the existing 10s htmx tick; a failed refresh keeps the last values and shows the existing global "Stale data" indicator (003 §2.11).
    - [ ] The map and timeline are usable on mobile per 003 §2.10 (no horizontal page scroll ≥360px, touch targets ≥44px, the map scales to viewport width).

---

## 3. Scope and Boundaries

### In-Scope

- Per-client connection timeline (online/offline bands, total connected time, session count) in the Clients expand panel.
- Online/last-seen status including a human-readable "last seen" for offline peers.
- Session inference from handshake gaps.
- An offline, self-contained world map of peers positioned by embedded GeoLite2 lat/long, with online/offline encoding and per-marker detail.
- Reuse of the existing time-range selector, retention, refresh model, and mobile rules.

### Out-of-Scope

- **Any new outbound network call** — the map is offline; geolocation stays the embedded GeoLite2 lookup (no external geocoding, no map tiles/CDN).
- **Heavyweight map dependencies** (Leaflet + online tiles, Mapbox, Google Maps) — they violate the offline/tunnel-only constraint.
- **Alerts / notifications** on connection events — that's spec 007.
- **Write/control operations** — still no add/remove/regen, no service control (unchanged from 002/003).
- **Failed-handshake / rejected-connection logging** — out of scope as in 003.
- **Long-term history beyond the existing SQLite retention** — no new long-term datastore.
- **In-band auth / HTTPS / public exposure** — still VPN-gated `http://172.16.15.1:8080`.
- **Config download (004), binary distribution (005), alerts (007)** — separate specifications.
