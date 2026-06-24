# Functional Specification: Web Dashboard v2 — admin views, tabs, longer time range

- **Roadmap Item:** Not yet on the roadmap (v3 of 002-web-dashboard was the prior shipped iteration; this spec follows on)
- **Status:** Completed
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The dashboard from spec 002 (v3) ships as a single-page summary at `http://172.16.15.1:8080`. After a few weeks of operator use, gaps are clear:

- **Bandwidth investigations are blind.** Only aggregate rx/tx is visible. When the VPN feels slow, the operator can't answer "which peer is consuming what?" without SSH-ing in and running `wg show wg0 dump`.
- **No sense of where peers are connecting from.** A peer connecting from an unexpected country is an interesting signal for a solo-operator VPN, and right now there's no way to see it.
- **Host triage still requires SSH.** Disk fill, runaway processes — neither is visible on the dashboard. The original spec de-scoped disk; in practice it's the second thing the operator checks when something feels off.
- **The page is full.** Adding more cards to the existing single-page layout would push the most-used data below the fold on mobile.
- **24-hour window is too short** for "is this a pattern or a one-off?" questions.

This spec extends the existing dashboard with admin-focused detail and reorganizes the layout into a six-tab structure. It keeps every constraint from v3: read-only, VPN-only access, no in-band auth, mobile-friendly, single Go binary deployed via the existing CI/SSM pipeline.

**Success looks like:** the operator can identify a noisy peer, confirm where peers are connecting from, see whether the host is healthy, and answer the same questions about last week — all without SSH and from any device.

---

## 2. Functional Requirements (The "What")

### 2.1 Tabbed navigation

- **As the operator, I want to** navigate the dashboard via tabs, **so that** related data lives together and the most-used view loads quickly.
  - **Acceptance Criteria:**
    - [x] Six tabs at the top of the page: **Overview**, **Clients**, **System**, **Network**, **Events**, **About**.
    - [x] **Overview** is the default tab on first load.
    - [x] The active tab persists in the URL fragment (e.g. `/#clients`) so refresh and shared links land on the same tab.
    - [x] On viewports below 600px the tab bar becomes a horizontally scrollable row of pills with no horizontal page scroll.
    - [x] Switching tabs does not trigger a full page reload — htmx swaps the tab-scoped fragment.

### 2.2 Overview tab (compact at-a-glance)

- **As the operator, I want to** see headline VPN health at a glance, **so that I can** answer "is everything OK?" in under 5 seconds.
  - **Acceptance Criteria:**
    - [x] Server endpoint card (existing): public IP, UDP port, server public key + copy button.
    - [x] Service status card (existing): Running / Stopped indicator + last-restart timestamp.
    - [x] Uptime card (existing): `wg-quick@wg0` uptime in human form, or "Service down" in red.
    - [x] **New** client-count summary card: "**N online** / M total" with green/grey coloring.
    - [x] Current CPU% and memory% large numeric values.
    - [x] Current rx/tx rate (e.g. "1.2 MB/s in / 480 KB/s out").
    - [x] No 24h charts on this tab (moved to System / Network); no full client list (moved to Clients); no event log (moved to Events).

### 2.3 Clients tab

- **As the operator, I want to** see each client's traffic and where they're connecting from, **so that I can** spot bandwidth hogs and unexpected geographies.
  - **Acceptance Criteria:**
    - [x] Table rows for every configured client: name, WG IP, online/offline indicator, last-handshake ("3 min ago"), cumulative rx, cumulative tx, peer public-IP endpoint, **geolocation** (country flag + city, or "—" when unresolvable).
    - [x] Clicking a row expands an inline panel below the row showing a **per-client 24h rx/tx chart** and the client's **p95 throughput over the selected time range**.
    - [x] Only one row may be expanded at a time; clicking another row collapses the previous.
    - [x] Geolocation comes from an offline MaxMind GeoLite2-City lookup bundled with the dashboard. RFC1918 / unresolvable addresses render as "—".
    - [x] Empty state when no clients configured: "No clients configured. Add via `terraform/dev/main.tf`."

### 2.4 System tab

- **As the operator, I want to** see host-level health, **so that I can** triage CPU, memory, disk, and process problems without SSH.
  - **Acceptance Criteria:**
    - [x] Current CPU% and memory% large numeric values.
    - [x] 24h CPU% and memory% trend charts, ≥1-min resolution, with a **time-range selector** (1h / 6h / 24h / 7d) above the chart pair.
    - [x] **Disk usage card**: one row per mounted filesystem (excluding `tmpfs`, `devtmpfs`, `overlay`, `squashfs`) with mount path, used / total in human form, and a percentage-full bar. Bars colored amber ≥80%, red ≥95%.
    - [x] **Top-5 processes by CPU%** table covering **all users / processes on the host** (no allow-list, no exclusion): PID, user, CPU%, mem%, command (truncated to 60 chars). Refreshes on the same 10s tick.

### 2.5 Network tab

- **As the operator, I want to** see interface-level traffic detail, **so that I can** monitor bandwidth and notice packet errors.
  - **Acceptance Criteria:**
    - [x] Current rx/tx rate large numerics (also on Overview).
    - [x] 24h aggregate rx and tx trend charts, ≥1-min resolution, with the same 1h / 6h / 24h / 7d time-range selector as the System tab.
    - [x] **WireGuard interface stats card**: connected-peer count, total rx packets, total tx packets, rx errors, tx errors, rx dropped, tx dropped.
    - [x] **Aggregate traffic total** card showing rx and tx volume over the selected time range (e.g. "Last 24h: 4.3 GB in / 8.1 GB out").

### 2.6 Events tab

- **As the operator, I want to** see a longer history of handshake events, **so that I can** spot reconnect patterns.
  - **Acceptance Criteria:**
    - [x] Event list of the most recent **50** handshake events (raised from the 10 cap on the prior single-page view, because this tab has dedicated room).
    - [x] Each row: timestamp, client name, event type ("handshake").
    - [x] Empty state: "No recent handshakes."

### 2.7 About tab

- **As the operator, I want to** see what's running and where, **so that I can** confirm versions and copy server values for client setup.
  - **Acceptance Criteria:**
    - [x] EC2 instance metadata card: instance type, AZ, AMI ID, public IP.
    - [x] Dashboard binary metadata card: build SHA (short), build timestamp, Go version.
    - [x] Kernel / OS card: kernel version, distro release.
    - [x] Server WireGuard public key with copy button (also on Overview's server-info card).

### 2.8 Time-range selector

- **As the operator, I want to** zoom in to recent activity or out to a week, **so that I can** judge whether a spike is a pattern or a blip.
  - **Acceptance Criteria:**
    - [x] Time-range selector visible on System and Network tabs only. Options: **1h**, **6h**, **24h**, **7d**.
    - [x] Default selection on first load is **24h** (matches v3 behavior).
    - [x] Selection persists in the URL fragment alongside tab state (e.g. `/#system?range=7d`).
    - [x] 7d requires extending dashboard SQLite retention from 25h to ~8 days. The database file size budget grows to roughly 20 MB (still well under any operational threshold).
    - [x] When 7d is selected and the database has not yet accumulated 7 days of samples, the chart shows however much exists with no error.

### 2.9 Dark-mode toggle

- **As the operator, I want to** flip between light and dark themes, **so that** the dashboard is comfortable on my phone at night.
  - **Acceptance Criteria:**
    - [x] Toggle button in the header (visible on all tabs).
    - [x] On first load the theme matches the browser's `prefers-color-scheme` media query.
    - [x] Manual override persists in `localStorage` and survives reload.
    - [x] All chart colors honor the active theme (background, gridlines, axis labels, line series).

### 2.10 Mobile responsiveness

- **As the operator, I want to** use every tab from my phone, **so that I can** keep the same checks I do from a laptop.
  - **Acceptance Criteria:**
    - [x] Same constraints as 002 §2.9 apply across all new tabs: no horizontal scroll ≥360px, single-column re-flow <600px, touch targets ≥44px.
    - [x] Tab bar: pill row scrollable horizontally on narrow viewports.
    - [x] Per-client expand panel stacks vertically below its row on narrow viewports (no overlapping content).
    - [x] Disk-usage and process-list rows are scannable on mobile (no truncation that hides the percentage / CPU figure).

### 2.11 Refresh behavior

- **As the operator, I want to** see live values without clicking refresh, **so that** the dashboard reflects current state.
  - **Acceptance Criteria:**
    - [x] Auto-refresh stays at 10 seconds via htmx polling (no SSE / WebSocket change).
    - [x] If a refresh fails, the previously rendered values remain visible and a global "Stale data" indicator is shown until the next successful refresh — same model as v3 §2.8.

---

## 3. Scope and Boundaries

### In-Scope

- Tabbed layout (Overview / Clients / System / Network / Events / About) with URL-fragment persistence.
- Per-client traffic chart + p95 in the Clients tab.
- Offline GeoLite2-City lookup for peer endpoints.
- Disk usage card and Top-5 process list (all users / no exclusion) in the System tab.
- WireGuard interface stats + aggregate traffic in the Network tab.
- Time-range selector (1h / 6h / 24h / 7d) on System and Network tabs.
- Extending SQLite retention from ~25h to ~8 days.
- Dark-mode toggle with `prefers-color-scheme` default + `localStorage` override.
- Mobile responsiveness for all new content.

### Out-of-Scope

- **Write / control operations** — no client add/remove/regen, no service restart, no `terraform apply` from the UI (unchanged from 002 v3).
- **In-band authentication** — still VPN-gated; no Basic auth or SSO.
- **HTTPS / public domain** — still `http://172.16.15.1:8080` over the WG tunnel.
- **Failed-handshake / firewall-rejection logging.**
- **Boot diagnostics tail / cloud-init log viewer.**
- **Audit log of SSM deploys** (would need new IAM perms; postponed).
- **SSE / WebSocket push** — htmx polling stays.
- **Sortable / filterable client list.**
- **CSV export of metrics.**
- **Per-card stale indicator** (the global one stays); per-card freshness deferred.
- **Other roadmap items** — addressed in their own specifications.
