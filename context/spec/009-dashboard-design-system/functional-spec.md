# Functional Specification: Dashboard Design System & Responsive Refresh

- **Roadmap Item:** Not yet on the roadmap (UX follow-on to the 002–008 dashboard work)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The dashboard (specs 002–008) is functionally complete — server/peer status, throughput, connection history, an offline geo map, alerting, and runtime webhook management — but its **visual design is generic and its responsiveness is brittle**. The palette is the textbook `#2563eb` blue on light-grey, typography is the default system-font stack, backgrounds are flat solids, and the layout is an auto-fit card grid with a handful of `@media` breakpoints rather than fluid sizing (the same brittleness that caused the Clients-tab clipping regression fixed in spec 006).

This spec applies a single, cohesive **design system** across the whole dashboard and reworks the layout to be **fluid across every screen size** — without changing any feature, data, or behavior. The aim is a **refined, distinctive** interface (per the `frontend-design` skill): characterful and memorable, but with legibility and information density kept paramount for an operations tool. Because the repository is heading toward open-source, a polished, professional dashboard is also part of the project's credibility.

Every hard constraint from 002/003 stays: **fully offline** (no external fonts, CDNs, or tiles — all assets `go:embed`-vendored), **no SPA or build step** (`html/template` + htmx + Chart.js), **dark mode**, **VPN-only / no auth**, and **functional parity** — this is a design pass, not a feature change.

**Success looks like:** an operator opening the dashboard on a phone, a laptop, or an ultrawide monitor sees a coherent, distinctive, legible interface that scales smoothly with no clipping or wasted space, in both light and dark themes — and a first-time visitor (post open-source) immediately reads it as a considered, professional tool rather than a default template.

---

## 2. Functional Requirements (The "What")

### 2.1 A single, cohesive design system

- **As the operator, I want** every tab and card to share one visual language, **so that** the dashboard feels designed, not assembled.
  - **Acceptance Criteria:**
    - [ ] A single source of truth defines the design tokens — color, typography scale, spacing, radii, elevation/shadows, borders, and motion — consumed by **all** surfaces (the 6 tabs and every card fragment).
    - [ ] No card or tab uses one-off colors, spacing, or font sizes outside the token system; visual treatment of equivalent elements (cards, headings, pills, tables, buttons, form controls) is consistent everywhere.
    - [ ] Both **light and dark** themes are first-class: every token has a defined value in each theme, and the theme toggle continues to work with no unstyled/low-contrast elements in either.

### 2.2 Distinctive, embedded typography

- **As the operator, I want** the dashboard to read as intentionally typeset, **so that** it escapes the generic default-font look.
  - **Acceptance Criteria:**
    - [ ] The dashboard uses **self-hosted, embedded** fonts (vendored via `go:embed`, served from `/static/`) — at least one distinctive display/heading face and a complementary body face, with a **monospace** face for technical/numeric data (keys, IPs, byte counts, timestamps).
    - [ ] **No external font request** is made (no Google Fonts / CDN); the offline guarantee is preserved and provable (no outbound requests on load).
    - [ ] A deliberate **type scale** (sizes, weights, line-heights, letter-spacing) is applied consistently; headings, body, labels, and monospace data are visually distinct and hierarchical.
    - [ ] Embedded font files are subset/`woff2`-compressed to keep the binary size increase reasonable, and their licenses permit redistribution (recorded in `VENDORED.txt`). `[NEEDS CLARIFICATION: exact font families to be proposed and chosen during the design/technical phase (open-licensed, OFL/permissive); record the final pick + license here.]`

### 2.3 Refined visual identity (color & atmosphere)

- **As the operator, I want** a distinctive, calm palette with depth, **so that** the dashboard is pleasant to watch and not a flat default.
  - **Acceptance Criteria:**
    - [ ] The palette moves off the generic textbook blue to a **cohesive, intentional** scheme (a considered dominant tone with sharp, sparing accents), distinct in light and dark.
    - [ ] Surfaces have **atmosphere/depth** rather than flat solid fills (e.g. subtle layering, considered borders/shadows, gentle texture/gradient where appropriate) — applied with restraint suited to an ops tool.
    - [ ] **Semantic status colors** (online/offline, active/inactive, success/warning/danger/info, firing/recovered) remain clearly distinguishable and accessible in both themes, and are mapped through the token system (no ad-hoc status colors).

### 2.4 Fluid responsiveness across all sizes

- **As the operator, I want** the dashboard to scale smoothly on any device, **so that** it's equally usable on a phone and an ultrawide monitor.
  - **Acceptance Criteria:**
    - [ ] Layout, type, and spacing scale **fluidly** (e.g. `clamp()`-based and container-aware) from **~360px phone → tablet → laptop → ultrawide**, not via a couple of fixed breakpoints.
    - [ ] At **no width** is content clipped, nor is there horizontal **page** scroll ≥360px; wide tables/maps degrade gracefully (scroll within their card or reflow), and full-width surfaces don't leave large dead space on very wide screens (sensible max content width / multi-column use).
    - [ ] Touch targets are **≥44px** and interactive controls remain comfortably usable on a handset (per 003 §2.10).
    - [ ] The existing tab/card structure and the geo map's projection alignment continue to work at every size (no regressions to the 006 timeline/map).

### 2.5 UX refinements (clarity & feedback)

- **As the operator, I want** clearer navigation and state feedback, **so that** I can read status and act with confidence.
  - **Acceptance Criteria:**
    - [ ] Tab navigation has a clear **active/current** indication and obvious focus/hover affordances; the active tab is unambiguous.
    - [ ] **Loading, empty, error, and stale-data** states are visually consistent and clearly communicated across cards (the existing "Stale data" indicator and empty states are restyled into the system, not removed).
    - [ ] Interactive actions (e.g. the webhook Set/Test/Revert, config downloads, range selector) give clear visual **feedback** (hover/active/disabled/result states) within the system.
    - [ ] Information **density** is tuned for an ops tool — scannable, with clear hierarchy — and primary status (is it up? who's connected? anything firing?) is immediately legible on the Overview.

### 2.6 Motion (subtle and purposeful)

- **As the operator, I want** tasteful motion, **so that** the interface feels responsive without being distracting.
  - **Acceptance Criteria:**
    - [ ] Motion is **CSS-only**, subtle, and purposeful — e.g. a brief tab-swap/page-load reveal and smooth state/hover transitions — concentrated on high-impact moments rather than scattered everywhere.
    - [ ] All motion respects **`prefers-reduced-motion`** (animations reduce/disable when the user requests it).
    - [ ] Motion never delays or obscures live data (the 10s refresh and chart updates stay snappy).

### 2.7 Accessibility

- **As any user, I want** the dashboard to meet baseline accessibility, **so that** it's usable and legible for everyone.
  - **Acceptance Criteria:**
    - [ ] Text and essential UI meet **WCAG AA** contrast in both themes.
    - [ ] All interactive elements have visible **focus** states and remain keyboard-operable; semantic structure (headings, landmarks, table semantics) is preserved or improved.
    - [ ] Color is **not the sole** carrier of meaning for status (pair with text/icon/shape).

### 2.8 Constraints preserved (no regressions)

- **As the maintainer, I want** the redesign to keep every existing guarantee, **so that** nothing about how the dashboard works changes.
  - **Acceptance Criteria:**
    - [ ] **Fully offline:** no external fonts, CDNs, tiles, or scripts are introduced; all new assets are `go:embed`-vendored and served from `/static/`.
    - [ ] **No SPA / no build step:** still server-rendered `html/template` + htmx partials + Chart.js; no front-end framework or bundler is added.
    - [ ] **Functional parity:** every tab, card, route, the 10s htmx tick, dark-mode toggle, geo map, charts, and alerting/webhook UI behave exactly as before — only their appearance/layout changes. No data, API, or behavior change.
    - [ ] VPN-only / no-auth posture is unchanged.

---

## 3. Scope and Boundaries

### In-Scope

- A unified design-token system (color, type, spacing, radii, elevation, motion) applied across **all 6 tabs and every card**.
- Distinctive **embedded** typography (display + body + monospace), offline, with a deliberate type scale.
- A refined, distinctive **palette and visual atmosphere**, light and dark.
- **Fluid, container-aware responsive** layout from ~360px phone to ultrawide.
- UX refinements: navigation/active states, consistent loading/empty/error/stale states, action feedback, density/hierarchy tuning.
- Subtle **CSS-only motion** with reduced-motion support, and **WCAG-AA** accessibility.

### Out-of-Scope

- **Any feature, data, or behavior change** — no new tabs, metrics, endpoints, or controls; no change to alerting/webhook/geo/history logic.
- **Backend changes** beyond embedding the new font/asset files and minimal template markup/class adjustments needed to apply the design system.
- **Switching the rendering model** — no SPA, no React/Vue, no CSS framework/bundler, no charting-library swap (Chart.js stays).
- **Changing the offline, VPN-only, no-auth posture.**
- **Final font/palette selection mechanics** beyond recording the chosen, license-cleared assets — exact families are recommended in §4 and finalized in the design/technical phase.

---

## 4. Recommendations

These are non-binding starting points for the design/technical phase (and a `frontend-design`-led mockup pass), not commitments:

- **Aesthetic direction — "precision instrument."** A refined, technical readout feel that suits a VPN/networking tool: a confident monospace for all numeric/technical data (IPs, keys, byte rates, timestamps), a precise grid, restrained but characterful headings, and calm surfaces with subtle depth. Distinctive without sacrificing density or legibility.
- **Typography (open-licensed, embeddable).** Pair a characterful but legible display/sans for headings/labels with a strong technical **monospace** for data; deliberately avoid the over-used defaults the skill flags (Inter/Roboto/Arial, and even the now-ubiquitous Space Grotesk). Choose **SIL OFL / permissively-licensed** families so they can be vendored and redistributed; **subset to `woff2`** and only the weights used to keep the binary increase small. Final families to be proposed with mockups.
- **Palette.** Move off textbook blue to a cohesive scheme with one dominant tone + sparing sharp accents; verify every semantic state color against WCAG AA in both themes before adopting.
- **Fluid sizing.** Build a `clamp()`-based type and spacing scale plus container-aware layouts (container queries where supported, with sane fallbacks), so the existing per-tab grids reflow smoothly instead of relying on fixed breakpoints — and cap content width on ultrawide to avoid dead space.
- **Process.** Given this is heavily visual, run a `frontend-design`-led mockup/exploration for the aesthetic direction, type, and palette **before** mass-applying it, so the token system is settled before it cascades across 25+ card fragments. The brittle responsive areas (the auto-fit `#tab-body` grid, wide tables, the geo map) are the highest-value first targets.
