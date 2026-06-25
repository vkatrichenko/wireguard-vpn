# Functional Specification: Geo Map Zoom & Legibility

- **Roadmap Item:** Not yet on the roadmap (legibility/interaction follow-on to 006-connection-history-geo-map)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 006 added an offline "Peer locations" world map: markers projected onto an embedded SVG outline. In practice it doesn't answer the question it exists for — **"where is this peer?"** On a small full-globe map a single marker is a large dot you can't place precisely; you can't tell which **country** (let alone city) a peer is in. Two defects make it worse:

- The marker renders **far larger than intended** — a colored blob covering a whole region — so even its approximate position is misleading (the visible dot should be ~14px inside a transparent 44px touch target, but the fill leaks to the full 44px box).
- The **"No mappable peers." empty state can show at the same time as a rendered marker**, which is contradictory and confusing.

This spec makes the map actually legible: fix the two defects, and add **zoom and pan** so the operator can zoom in — at least to country level — and move around to read a peer's location. It stays within every 006 constraint: **fully offline** (no map tiles, no Leaflet/Mapbox, no external requests — the map remains the embedded SVG), dark-mode aware, mobile-friendly, and refreshing on the existing 10s tick.

**Success looks like:** the operator can glance at the map, zoom into the region a peer is in, and confidently read its country/city — with markers that sit precisely on their location and an empty state that only appears when there genuinely are no mappable peers.

---

## 2. Functional Requirements (The "What")

### 2.1 Marker legibility (defect fixes)

- **As the operator, I want** markers to sit precisely on their location, **so that I can** trust where a peer is.
  - **Acceptance Criteria:**
    - [ ] A marker's **visible dot** is small enough to pin a location (≈ its intended ~14px), not a large blob filling its touch-target box; the **≥44px touch/click target is preserved** (003 §2.10) via transparent hit-area, not a bigger visible dot.
    - [ ] The **"No mappable peers." empty state is shown only when there are no markers**; it is never visible at the same time as a rendered marker (including across failed-fetch / stale-data ticks).
    - [ ] Online/offline color + opacity, the count badge for co-located peers, and the hover/tap tooltip (name, city/country, online/last-seen) continue to work.

### 2.2 Zoom & pan

- **As the operator, I want** to zoom into and pan around the map, **so that I can** see a peer's country/region clearly.
  - **Acceptance Criteria:**
    - [ ] The operator can **zoom in and out** via on-screen **`+` / `−` controls** and via **scroll-wheel (desktop) and pinch (touch)**; zoom is **bounded** (a sensible min = fit-to-frame, and a max that comfortably resolves country level — proposed ~6–8×).
    - [ ] The operator can **pan** by dragging (mouse) or one-finger drag (touch) while zoomed in; panning is **bounded** so the map can't be lost off-screen.
    - [ ] A **reset / fit control** returns the map to the default full-globe view.
    - [ ] **Markers stay correctly positioned** on their geographic location at every zoom level and pan offset (they scale/move with the map, not drift); the dot's visual size stays readable when zoomed (it should not balloon with the zoom factor).
    - [ ] Zoom/pan state is **transient** (view-only) — it does not need to persist across reloads or the 10s data refresh, and a data refresh must **not reset** an in-progress zoom/pan.

### 2.3 Offline & constraints preserved

- **As the maintainer, I want** zoom to keep every 006 guarantee, **so that** nothing about the map's offline/security posture changes.
  - **Acceptance Criteria:**
    - [ ] Zoom/pan is implemented purely as a **transform of the embedded SVG** (viewBox or CSS transform scale+translate) — **no online map tiles, no Leaflet/Mapbox, no external request** of any kind.
    - [ ] The map remains **dark-mode aware** and adopts the dashboard design system styling (the zoom controls match the 009 token system).
    - [ ] **Mobile:** pinch-zoom + drag-pan work on a handset; controls are ≥44px touch targets; no horizontal **page** scroll is introduced (map interactions stay within the card).
    - [ ] Zoom transitions respect **`prefers-reduced-motion`**; the 10s tick, clustering, and tooltips keep working while zoomed.

---

## 3. Scope and Boundaries

### In-Scope

- Fixing the oversized-marker defect and the empty-state-with-marker defect.
- Zoom (buttons + scroll/pinch, bounded) and pan (drag, bounded) on the existing offline SVG map, with a reset/fit control.
- Keeping markers correctly placed and readable through zoom/pan; preserving tooltips, clustering, online/offline encoding, and the 10s refresh.
- Dark-mode + mobile + design-system-styled controls; reduced-motion support.

### Out-of-Scope

- **Online map tiles / Leaflet / Mapbox / Google Maps** — still excluded (offline constraint, unchanged from 006).
- **Changing the projection or the base SVG** (still the 006 equirectangular Natural-Earth outline) — beyond what zoom/pan transforms require.
- **Street-level / sub-city precision** — geolocation stays the embedded DB-IP **city-level** lite database; zoom reveals what that data supports, not finer.
- **New geo data, connection routing/animation, or a click-marker-to-zoom-to-country interaction** — manual zoom + pan only for v1 (click-to-zoom can be a later enhancement).
- **Persisting zoom/pan state** across reloads.
- **Any backend/API change** — `/api/geo` and the geo data are unchanged; this is a front-end (SVG/JS/CSS) feature.

---

## 4. Recommendations

Non-binding starting points for the technical phase:

- **Transform model.** Either animate the SVG `viewBox` (clean math, crisp at any zoom) or wrap the SVG + marker overlay in a single transformed element (`transform: scale() translate()`); the latter keeps the existing percent-positioned marker overlay aligned for free. Counter-scale the marker dots so they stay ~14px regardless of zoom factor.
- **Bounds.** Clamp zoom to `[fit, ~8×]` and pan so at least part of the map stays in view; provide `+ / − / reset` buttons plus wheel/pinch. Debounce wheel zoom; use pointer events for unified mouse/touch drag + pinch.
- **Sequencing.** The two **defect fixes are independent and small** — ship them first (they make the current map readable immediately); the **zoom/pan** is the larger interaction slice on top.
- **Design.** Style the zoom controls with the 009 design tokens (this spec lands after/alongside 009's component work); keep them unobtrusive and reduced-motion-friendly.
