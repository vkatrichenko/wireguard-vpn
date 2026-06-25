# Technical Specification: Dashboard Design System & Responsive Refresh

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

This is a **CSS-led, token-driven** refresh with **no rendering-model change**. The dashboard already centralises its look in a `:root` / `[data-theme="dark"]` custom-property block in `web/static/app.css`, and the templates already use semantic class hooks (`.card`, `.status-pill`, `.client-table`, `.overview-grid`, …). So the bulk of spec 009 is: **expand the token system** (fluid type/space scales, elevation, motion, embedded font families), **reorganise `app.css` into cascade layers** for maintainability, **add self-hosted embedded fonts**, and **rework the responsive layer from a single breakpoint to a fluid (`clamp()` + container-aware) system** — then apply it across the 6 tabs and ~25 card fragments with **minimal template edits** (mostly adding class hooks).

Because the result is heavily visual and cascades across many fragments, the work starts with a **`frontend-design`-led offline mockup** to settle the aesthetic direction, type, palette, and token values **before** they are extracted into `app.css` and applied broadly. Everything stays **fully offline** (fonts vendored via the existing `go:embed all:web`), **no SPA/build step**, **Chart.js retained**, and **functional parity** (only appearance/layout changes).

Go changes are minimal-to-none: new assets live under `web/static/` and are picked up by the existing embed; template edits are class/markup-only and must keep every render test green.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Token architecture & CSS organisation

| Concern | Approach |
|--------|----------|
| Cascade control | Introduce CSS `@layer` ordering — `tokens, base, components, utilities, responsive` — in the single embedded `app.css` (keep one file; no `@import` chain, no bundler). Re-home the existing numbered sections into these layers. |
| Tokens (extend `:root` + `[data-theme="dark"]`) | Add: a **fluid type scale** (`--fs-*` via `clamp()`), a **spacing scale** (`--space-*`, some fluid), **elevation/shadow** tokens, **border/line** tokens, **motion** tokens (`--ease-*`, `--dur-*`), **font-family** tokens (`--font-display`, `--font-body`, `--font-mono`), and **layout** tokens (`--content-max`, container breakpoints). Every token defined in **both** themes. |
| Single source of truth | All component rules consume tokens only — no literal colors/sizes/fonts in component CSS (enforced by review + a lint-style grep in verification). |

### Typography — self-hosted embedded fonts

- Vendor subset **`woff2`** files into `web/static/fonts/` (covered by the existing `//go:embed all:web`); declare them with `@font-face` (`font-display: swap`, `src: url("/static/fonts/…woff2") format("woff2")`, **no `local()`/external `src`**). Wire `--font-display` / `--font-body` / `--font-mono` to them.
- **Offline + size:** no Google Fonts / CDN. **Subset** each face (Latin + the glyphs actually used) and ship **`woff2`** only, to keep the binary delta small (target: low tens of KB total). Record each font's **source URL, version, license (SIL OFL / permissive), and sha256** in `web/static/VENDORED.txt`, and commit the upstream license text. Final families chosen during the mockup phase (functional spec §2.2 `[NEEDS CLARIFICATION]`).
- Subsetting is a **design-time** step (e.g. `fonttools`/`glyphhanger`) producing committed artifacts — **not** a runtime or build-time dependency; the committed `woff2` are the source of truth (same philosophy as the vendored `world.svg`).

### Fluid responsive system

- **Fluid scales:** replace fixed `font-size: 14px` and the lone `@media (max-width:600px)` with a `clamp()`-based type scale and fluid spacing, so type/space interpolate with viewport width from ~360px to ultrawide.
- **Container-aware layout:** use **container queries** (`@container`) for card-internal reflow (e.g. the client table, the geo card, multi-stat cards) with **`@media` fallbacks** for older engines; keep a small set of **semantic** breakpoints rather than many ad-hoc ones.
- **`#tab-body` grid:** rework the auto-fit grid (currently `repeat(auto-fit, minmax(300px,1fr))`) into a fluid, clamp-driven track sizing that won't column-split surfaces that must stay full-width (the 006 clipping class — `grid-column: 1 / -1` — is folded into the system). Wide tables/maps scroll/reflow **within their card**, never the page.
- **Ultrawide:** keep a capped `--content-max` (today `body { max-width: 1400px }`) but tune it / allow controlled multi-column so very wide screens don't leave dead space.
- **Geo map:** preserve the 2:1 `aspect-ratio` + projection math (006) exactly — the map frame stays size-capped and centered; only its chrome is restyled.

### Component restyle & template application

- **CSS-first:** restyle by component family through the token/component layers — cards, type/headings, status & state pills, tables, buttons, **forms** (webhook Set/Test/Revert), the range selector, **nav/tabs**, charts containers, and the **loading/empty/error/stale** states. The existing semantic classes mean most fragments need **no markup change**.
- **Minimal template edits:** add class hooks only where structure demands it (e.g. a nav wrapper for the active-tab indicator, a stat-grid wrapper). Every edited fragment must keep its render/partial test green; structural assertions (ids, semantic tags) are preserved.
- **Charts bridge:** Chart.js already reads CSS vars via `charts.js` (006); extend that bridge so charts adopt the new palette **and** the embedded font for axis/labels. No charting-library change.

### Motion & accessibility

- **CSS-only** motion in the `responsive`/dedicated motion layer: a subtle, one-shot **tab-switch reveal** and smooth hover/state transitions. **Critical constraint:** the 10s htmx tick re-swaps the active tab's partial into `#tab-body`, so an entrance animation on tab-body children would **re-fire every 10s and flicker**. Motion must be scoped to **user-initiated tab changes** (e.g. a one-shot class toggled by `tabs.js` on hashchange), **not** the periodic refresh. All motion gated by **`prefers-reduced-motion: reduce`**.
- **Accessibility:** verify **WCAG AA** contrast for text + state colors in both themes before adopting the palette; ensure visible **focus** states, keyboard operability, and that status is never color-only (text/icon/shape too).

### Process — mockup before mass-apply

- **Slice 1 should be a standalone, offline `frontend-design`-led mockup** (a single self-contained HTML page exercising the proposed type/palette/tokens/components in light + dark, across widths) for visual review. Only after sign-off are the tokens extracted into `app.css` and applied tab-by-tab. This settles the system before it cascades across 25+ fragments and keeps the per-tab passes mechanical.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** `web/static/app.css`, the `web/static/fonts/` assets (new), `charts.js` theme bridge, `theme.js` (dark toggle), `tabs.js` (tab swap / motion hook), the 6 tab templates + ~25 card fragments, and `VENDORED.txt`. The existing `go:embed all:web` already covers new static assets.
- **Potential Risks & Mitigations:**
  - **10s-tick animation flicker.** *Mitigation:* scope entrance motion to user tab-changes only, never the periodic partial refresh; verify the active tab doesn't visibly re-animate every 10s.
  - **Binary-size growth from fonts.** *Mitigation:* subset + `woff2`, only used weights; report the KB delta; keep total in the low tens of KB.
  - **Scope across 25+ fragments / regressions.** *Mitigation:* token-driven CSS does the heavy lifting; render tests guard structure; restyle per-tab in reviewable passes; explicitly re-verify the **geo map projection**, **charts**, and **connection timeline** (006) at each pass.
  - **Container-query support.** *Mitigation:* `@media`/`clamp()` fallbacks so layout degrades gracefully on engines without `@container`.
  - **WCAG-AA on a new palette.** *Mitigation:* check contrast in both themes before committing the palette; pair color with text/icon for status.
  - **Dark-mode parity drift.** *Mitigation:* every token defined in both themes; a both-themes pass per component.
  - **Offline guarantee.** *Mitigation:* `@font-face` srcs are local `/static/fonts/` only; grep templates + CSS for any external URL; manual devtools "no outbound on load" check.
  - **Font licensing (open-source repo).** *Mitigation:* OFL/permissive only; commit license text; record in `VENDORED.txt`.

---

## 4. Testing Strategy

- **Structure/parity (automated):** all existing partial/render tests stay green — the redesign must not change template structure/semantics; add assertions for any new class hooks. `go test ./...`, `gofmt`/`go vet` clean, `make build` succeeds (note the binary-size delta from fonts).
- **Offline proof (automated):** grep `web/templates/` + `web/static/` for external resource URLs (none); assert every `@font-face` `src` is a local `/static/fonts/…` path and the `woff2` files are embedded.
- **Accessibility (semi-automated):** contrast-check key text/state pairs against **WCAG AA** in both themes; confirm focus-visible styles and keyboard operability.
- **Responsive + visual (manual gate):** there is no browser MCP, so the visual + fluid behavior is an operator check — load each tab at ~360px / tablet / laptop / ultrawide, in **light and dark**, with reduced-motion on and off: assert no clipping, no horizontal page scroll ≥360px, no ultrawide dead space, legible hierarchy, working active-tab indicator, and **no 10s-tick flicker**. Confirm charts, the geo map markers, and the timeline still render/align.
- **Mockup review:** the Slice-1 mockup is reviewed (light/dark, multiple widths) and signed off before mass application.
