# Technical Specification: Geo Map Zoom & Legibility

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Entirely a **front-end** change to the existing offline geo map (spec 006) — `dashboard/web/static/world-map.js`, the geo-map card in `web/templates/tabs/clients.html`, and the geo CSS in `web/static/app.css`. **No backend / `/api/geo` / geo-data change.** Three pieces:

1. **Two defect fixes** (small, ship first): the oversized marker (a `background`-shorthand call clobbering `background-clip`) and the empty-state that can show alongside a marker.
2. **Zoom & pan** implemented as a **CSS `transform: scale()+translate()` on a wrapper** that holds both the `<img>` basemap and the percent-positioned `.geo-markers` overlay, inside an `overflow:hidden` viewport. The basemap is an `<img>` (not inline SVG), so a wrapper transform is simpler and keeps the markers aligned for free (they're positioned in % of the wrapper); marker **dots are counter-scaled** so they stay a constant size as you zoom. Stays fully offline — pure transforms, no tiles.
3. **Controls & interaction** (`+`/`−`/reset buttons, wheel, pinch, drag-pan) with bounded zoom/pan, styled with the spec-009 design tokens, keyboard-operable, and `prefers-reduced-motion`-aware.

The pure geometry (projection + the new zoom/clamp math) stays as side-effect-free functions so the existing `dashboard/jstest/world-map.test.mjs` can unit-test them.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Defect fixes (Slice 1 — independent, immediately improves legibility)

| Defect | Cause | Fix |
|--------|-------|-----|
| Oversized marker | `applyMarkerColor` sets `marker.style.background = …` (the **shorthand**), which resets `background-clip` from the CSS `content-box` back to `border-box` — so the color fills the full 44px touch-target box instead of the 14px dot. | Set `marker.style.backgroundColor = …` (longhand) so the stylesheet's `background-clip: content-box` survives and the visible dot stays 14px (the 44px transparent hit-area is unchanged). |
| Empty state shows with a marker | `emptyEl.hidden = clusters.length > 0` runs **only in the fetch-success path**; a failed/stale tick (or the server cold-load default) can leave the "No mappable peers." text and a rendered marker visible together. | Derive empty visibility from the **actual rendered marker count** (`overlay.children.length`) after every `renderMarkers`, so the empty state and a marker can never coexist; a failed fetch keeps the last markers and leaves empty hidden. |

### Zoom / pan model

- **DOM:** restructure the geo card to `.geo-map-frame > .geo-viewport (overflow:hidden) > .geo-canvas > { img.geo-basemap, .geo-markers }`. The transform is applied to `.geo-canvas`; `.geo-empty`, the zoom controls, and the legend live outside `.geo-canvas` (overlaid on / below the viewport) so they don't scale.
- **Transform:** `.geo-canvas { transform: translate(tx px, ty px) scale(z); transform-origin: 0 0; }`. Markers keep their existing **percent** left/top (so they ride the transform and stay geographically pinned); each marker **dot is counter-scaled** (`transform: translate(-50%,-50%) scale(1/z)`) so its on-screen size is constant at any zoom (per functional spec §2.2 "dot should not balloon").
- **Pure helpers** (added to `world-map.js`, unit-tested): `clampZoom(z) → [MIN=1, MAX≈8]`; `clampPan(tx, ty, z, viewportW, viewportH) → bounded translate` so the scaled canvas can't be dragged entirely off-screen; `zoomAt(point, oldZoom, newZoom, pan) → pan'` to keep the cursor/pinch-midpoint anchored while zooming. `project`/`projectPercent`/`clusterPeers` are unchanged.
- **Re-render interaction:** the 10s `/api/geo` tick re-renders markers into the overlay; since markers are percent-positioned inside `.geo-canvas`, the **current transform persists** across re-renders (it lives on `.geo-canvas`, not the overlay) — the dots just need their counter-scale re-applied from the current `z`.

### Controls & interaction

- **Controls:** a small overlay control group on the viewport — `+`, `−`, and **reset/fit** buttons (`<button>`, ≥44px, keyboard-focusable, `:focus-visible` per 009). Styled with 009 tokens (`--card-bg-2`, `--elev-*`, `--radius`, `--signal` focus).
- **Pointer input** via Pointer Events (unified mouse/touch): single-pointer drag → pan; two-pointer → pinch-zoom about the midpoint. **Wheel** → zoom toward the cursor (debounced; `preventDefault` only inside the map so the page still scrolls elsewhere).
- **Bounds:** `z ∈ [1, ~8]`; `z=1` is fit-to-frame (reset returns here). Pan clamped via `clampPan`.
- **Motion:** zoom/pan apply via a short CSS `transition` on `transform`, **disabled under `prefers-reduced-motion`** (instant). Never blocks the 10s data refresh or tooltips.

### Component breakdown

| Component | Path | Change |
|-----------|------|--------|
| Map script | `web/static/world-map.js` | Defect fixes; the `.geo-canvas` transform state + `clampZoom`/`clampPan`/`zoomAt` pure helpers; pointer/wheel/pinch handlers; control wiring; marker counter-scale. |
| Geo card | `web/templates/tabs/clients.html` | Add `.geo-viewport`/`.geo-canvas` wrappers + the zoom control group; keep `.geo-empty`, legend, caption. (Update the geo-card render test for the new structure.) |
| Geo CSS | `web/static/app.css` | `.geo-viewport` (overflow:hidden), `.geo-canvas` (transform, transform-origin), control-group styling (009 tokens), marker counter-scale, reduced-motion. **Preserve** `.geo-map-frame` `aspect-ratio:2/1` + `max-width`. |
| JS tests | `dashboard/jstest/world-map.test.mjs` | Add unit tests for `clampZoom`/`clampPan`/`zoomAt` alongside the existing projection tests. |

---

## 3. Impact and Risk Analysis

- **System Dependencies:** the 006 geo map (`world-map.js`, the `world.svg` basemap, `/api/geo`, the percent-projection), and the spec-009 design tokens for the control styling. No backend dependency.
- **Potential Risks & Mitigations:**
  - **Markers drift from their location when zoomed.** *Mitigation:* keep markers percent-positioned **inside** the transformed `.geo-canvas` so they transform identically to the basemap; counter-scale only the dot's own size, not its position. Unit-test the projection + a zoomed-position assertion.
  - **Pan loses the map off-screen.** *Mitigation:* `clampPan` bounds translate to the scaled extent; reset/fit always recovers `z=1`.
  - **Touch/wheel hijacks page scroll.** *Mitigation:* `preventDefault` pinch/wheel **only** within the map element; single-finger drag pans only while zoomed in (otherwise lets the page scroll).
  - **The 10s re-render resets zoom or loses dot scaling.** *Mitigation:* transform lives on `.geo-canvas` (not re-rendered); `renderMarkers` re-applies the dot counter-scale from current `z`.
  - **Marker fix regressions.** *Mitigation:* the `backgroundColor` change is verified by the existing theme/marker behavior; the empty-state derivation is covered by a render-count assertion.
  - **Offline guarantee.** *Mitigation:* transforms only — no tiles, no external request; grep templates + CSS + JS for external URLs.
  - **Accessibility.** *Mitigation:* controls are real `<button>`s with focus states; markers remain focusable `<button>`s; reduced-motion respected. (The unrelated client-row `<tr>` keyboard gap flagged in spec 009 is **out of scope** here.)

---

## 4. Testing Strategy

- **Unit (jstest, pure):** `clampZoom` (bounds), `clampPan` (corners can't pull the map off-screen at several zooms), `zoomAt` (cursor stays anchored across a zoom step), and the existing projection tests — all cwd-independent `node --test`.
- **Marker/empty fixes:** assert (in JS test or by inspection) that `applyMarkerColor` no longer uses the `background` shorthand; the empty state derives from rendered marker count (a render-count toggle test).
- **Go render test:** the geo-map card renders the new `.geo-viewport`/`.geo-canvas` wrappers + the zoom control buttons; update the existing geo-map-card assertion; `go test ./...` green.
- **Offline:** grep `web/templates/` + `web/static/` (incl. `world-map.js`) for any external URL — none; `world.svg` + everything stays embedded.
- **Static build:** `gofmt`/`go vet` clean; `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` build OK.
- **Manual E2E (operator):** on the deployed dashboard — zoom in (buttons / wheel / pinch) to country level, confirm a peer's marker sits on the right country and the dot stays a sensible size; pan within bounds; reset/fit; verify no page-scroll hijack, dark-mode + mobile (pinch/drag, ≥44px), reduced-motion, and that the 10s refresh doesn't reset the view. Confirm zero outbound requests (devtools).
