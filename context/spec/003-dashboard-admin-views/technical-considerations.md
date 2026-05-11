# Technical Specification: Web Dashboard v2 — admin views, tabs, longer time range

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Spec 003 builds on the spec 002 (v3) dashboard rather than replacing it. The runtime stays a single Go binary on the WireGuard EC2, bound to `172.16.15.1:8080`, served from the same CI-pushed `/opt/wireguard-dashboard/bin/wireguard-dashboard`. No new Terraform resources, no new IAM permissions, no new external services.

The page becomes a six-tab shell. Each tab is its own htmx partial endpoint, polled every 10 seconds with the same `hx-trigger="every 10s"` model already in use. Tab and time-range selection persist in the URL fragment via a small JS shim so refresh + share-link land on the same view.

Per-client traffic + p95 reuse the existing `client_traffic` SQLite table (already populated by the poller, just not yet exposed). Disk, process list, and WireGuard interface stats are read directly from `/proc` — no `sudo` needed beyond what the v3 sudoers fragment already grants. Geolocation uses a vendored offline MaxMind GeoLite2-City database loaded via `embed.FS` so the binary stays one self-contained artifact.

SQLite retention bumps from 25h to **8 days** (~20 MB on disk at current sample cadence) so the new 7d time-range option has data to plot.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Architecture changes

| Layer | Change |
|---|---|
| Terraform | **None.** Existing IAM, EC2, SSM document, GH Actions deploy chain all unchanged. |
| Go runtime | Add three `/proc` readers (`disk`, `processes`, `netdev`), a `geoip` package, expand `server` with per-tab handlers, extend `db` retention and add per-client query helpers. |
| Frontend | Replace `web/templates/dashboard.html` single-content layout with a tab shell + per-tab fragment files. Add `theme.js`, `tabs.js` shims. |
| Storage | Reuse the four existing SQLite tables; only bump the prune cutoff. |
| External deps | Add `github.com/oschwald/geoip2-golang` for GeoLite2 lookup. Vendor `GeoLite2-City.mmdb` (~7 MB) under `internal/geoip/`. |

### 2.2 New Go packages

| Package | Path | Responsibility |
|---|---|---|
| `disk` | `dashboard/internal/disk/disk.go` | Parse `/proc/mounts`, call `unix.Statfs` on each entry, filter `tmpfs`/`devtmpfs`/`overlay`/`squashfs`, return `[]Mount{Path, Used, Total, PctFull}`. |
| `processes` | `dashboard/internal/processes/processes.go` | Walk `/proc/[pid]/`, read `stat` + `status` + `cmdline`. Compute CPU% via two-sample delta (own prior snapshot, mutex-guarded — same pattern as `proc.Service`). Sort by CPU%, return top-5. |
| `netdev` | `dashboard/internal/netdev/netdev.go` | Parse `/proc/net/dev` row for `wg0`: rx/tx bytes/packets/errs/drops. |
| `geoip` | `dashboard/internal/geoip/geoip.go` | Wrap `oschwald/geoip2-golang`. Load `embed.FS` `GeoLite2-City.mmdb` at startup. Expose `Lookup(ip net.IP) (Country, City string)`. RFC1918 / nil-result returns `"", ""`. |

### 2.3 Existing Go packages — touch list

| Package | Change |
|---|---|
| `db` | Bump `DefaultRetention` from `25h` to `8*24h + 1h`. Add `QueryClientTraffic(ctx, publicKey, from, to)` per-key variant (current method scans all). Existing schema unchanged. |
| `poller` | No change to sample cadence. Already writes `client_traffic` rows per peer per tick (used by the new Clients tab). |
| `server` | Split handlers per tab: `handlers_overview.go`, `handlers_clients.go` (expand for inline-row detail), `handlers_system.go`, `handlers_network.go`, `handlers_events.go`, `handlers_about.go`. Each registers `GET /partial/<tab>` that returns the tab body fragment. Existing `/partial/dashboard` becomes a thin alias of `/partial/overview` and is deprecated. |
| `serverinfo` | Add EC2 IMDSv2 reads for instance type, AZ, AMI ID (already pulls public IP). Add kernel + distro release readers (`uname -r`, `/etc/os-release`). |

### 2.4 HTTP API surface

| Method + Path | Returns | Notes |
|---|---|---|
| `GET /` | Full page shell (tab nav + initial Overview body) | Default tab = Overview. Reads URL hash on client side to optionally request a different initial tab. |
| `GET /partial/overview` | HTML fragment for the Overview tab | Reuses existing summary cards. |
| `GET /partial/clients` | HTML fragment for the Clients tab | Includes the client table. Expand-state lives in the URL hash + DOM. |
| `GET /partial/clients/{pubkey}/detail` | HTML fragment for one expanded client row | Per-client 24h chart canvas + p95 figure. |
| `GET /partial/system?range=1h\|6h\|24h\|7d` | HTML fragment for the System tab | Includes CPU/mem charts at the chosen range + disk + top-5 procs. |
| `GET /partial/network?range=1h\|6h\|24h\|7d` | HTML fragment for the Network tab | Rx/tx charts at the chosen range + WG interface stats + range-aggregate totals. |
| `GET /partial/events` | HTML fragment for the Events tab | Last 50 handshake events. |
| `GET /partial/about` | HTML fragment for the About tab | EC2 metadata + binary metadata + kernel/OS. |
| `GET /api/metrics/system?range=…` | JSON time-series for CPU/mem | Used by Chart.js; range param controls window. |
| `GET /api/metrics/traffic?range=…` | JSON time-series for rx/tx | Same shape pattern. |
| `GET /api/metrics/client/{pubkey}?range=…` | JSON time-series for one peer | New. |
| `GET /api/health` | `{"ok":true}` | Unchanged. |

Range parameter accepts only the four enum values; anything else returns 400.

### 2.5 Frontend structure

| File | Responsibility |
|---|---|
| `web/templates/dashboard.html` | Page shell: header (title + dark-mode toggle + stale pill), tab bar (six pills), `<main id="tab-body">`. Initial tab body rendered server-side. |
| `web/templates/tabs/overview.html` | Compact at-a-glance — references existing cards (server-info, service-status, uptime, system, network-rate) + new client-count summary. |
| `web/templates/tabs/clients.html` | Client table fragment. Each row has `hx-get="/partial/clients/{pubkey}/detail"` and `hx-target="#detail-{pubkey}"` so one click expands inline. |
| `web/templates/tabs/system.html` | CPU/mem charts + time-range selector + new disk card + new top-5 process card. |
| `web/templates/tabs/network.html` | Rx/tx charts + time-range selector + WG interface stats + aggregate totals. |
| `web/templates/tabs/events.html` | 50-row handshake table (existing card extended). |
| `web/templates/tabs/about.html` | New — EC2 + binary + kernel/OS metadata cards. |
| `web/templates/cards/disk.html` | New — disk usage table. |
| `web/templates/cards/processes.html` | New — top-5 process table. |
| `web/templates/cards/wg-iface-stats.html` | New — peer count + packet counters. |
| `web/templates/cards/aggregate-traffic.html` | New — "Last Xh: A in / B out". |
| `web/templates/cards/about-*.html` | New — EC2, binary, OS subcards. |
| `web/templates/cards/client-list.html` | Modified — adds geolocation column + per-row expand row. |
| `web/templates/cards/client-detail.html` | New — inline expand body (per-client chart + p95). |

### 2.6 Static assets

| File | Change |
|---|---|
| `web/static/app.css` | New CSS custom properties on `:root` for color tokens. `:root[data-theme="dark"]` overrides. New tab-bar styles (pill row, horizontal scroll on narrow). New expand-row layout. |
| `web/static/charts.js` | Read theme tokens at chart-init time so series colors honor light/dark. Wire time-range `<select>` to refetch the chart's `/api/metrics/*?range=` endpoint. |
| `web/static/theme.js` | New. Read `prefers-color-scheme`, apply `data-theme` attr, persist override in `localStorage`. |
| `web/static/tabs.js` | New. On load, read URL hash (`#system?range=7d`) → trigger htmx swap of the right tab body. On tab-pill click, push new hash. ~30 LOC, no framework. |
| `web/static/htmx.min.js`, `chart.umd.min.js`, `htmx-stale.js`, `chartjs-adapter-date-fns.bundle.min.js` | Unchanged. |
| `internal/geoip/GeoLite2-City.mmdb` | New vendored asset. ~7 MB. Refreshed by the maintainer on demand; no auto-update loop. |

### 2.7 Sample cadence and retention

- Poller cadence stays at 30 s for `system_metrics`, `traffic_metrics`, `client_traffic`. No new tables.
- `db.DefaultRetention` `25 * time.Hour` → `8*24 + 1` hours. Prune sweep cadence unchanged (1 h).
- Expected on-disk DB size with 2 clients at 30 s × 8 days ≈ 17 MB (`system_metrics` + `traffic_metrics` + `client_traffic` × peers + indexes).
- The first time the binary boots with the new retention, the prune sweep is a no-op (existing data is younger than the new cutoff).

### 2.8 Tab + range URL fragment scheme

- `https://172.16.15.1:8080/#overview` (default; empty hash also routes here)
- `…/#clients` — Clients tab, no expand
- `…/#clients?expand=<pubkey>` — Clients tab with one row pre-expanded
- `…/#system?range=7d` — System tab on 7-day range
- `…/#network?range=1h` — Network tab on 1-hour range

`tabs.js` parses the hash on `DOMContentLoaded` and on `hashchange`; it dispatches one htmx request per change. No server-side dependency on the fragment — the server reads only query-string `range=` from the partial URL.

### 2.9 Logic notes

**Per-client p95:** Compute as p95 of the per-sample throughput series (`Δrx_bytes / Δt`) over the selected range, in bytes/s. Implementation lives in `server/handlers_clients.go` or a small `internal/p95` helper; no SQLite extension required.

**Process CPU%:** Same delta model as `proc.Service` — keep a per-PID prior `utime + stime + cutime + cstime` and total `/proc/stat` jiffies in a singleton, recompute on each request, mutex-guarded. Top-5 is selected after the delta pass.

**Geolocation cache:** GeoLite2 lookup is fast (~µs) — no in-process cache needed. Looking up the same peer's endpoint 10×/min is fine.

**Theme color application:** Chart.js does not natively listen to CSS var changes. On theme toggle, `theme.js` calls a `window.__rebuildCharts()` hook exposed by `charts.js` that re-reads tokens and re-creates each chart in place.

---

## 3. Impact and Risk Analysis

### System Dependencies

- **`/proc` filesystem** — disk/process/netdev readers depend on Linux `/proc` semantics. Acceptable on Ubuntu 24.04 EC2; not portable to non-Linux dev hosts (existing `proc.Service` already has this constraint).
- **`oschwald/geoip2-golang`** — pure-Go module, preserves CGO_ENABLED=0 promise.
- **MaxMind GeoLite2 license** — Lite databases are licensed CC BY-SA 4.0; redistribution with attribution is permitted. Add `LICENSE-GeoLite2.txt` (or equivalent attribution line) alongside the embedded DB.
- **No new AWS permissions** — IAM unchanged from 002 v3.

### Potential risks and mitigations

| Risk | Mitigation |
|---|---|
| **Binary size grows ~7 MB** from embedded GeoLite2 DB. Pushes S3 artifact + SSM cp time up. | Acceptable: total binary stays well under 20 MB. SSM `aws s3 cp` is bandwidth-bound; an extra 7 MB on a t3.micro tunnel is ~1 s. |
| **GeoLite2 DB goes stale** — MaxMind ships weekly updates; bundled snapshot may lag months. | Document refresh procedure in `internal/geoip/README.md` (or a comment): replace the `.mmdb` file and rebuild. No auto-update loop. |
| **Process list information leak** — Top-5 by CPU exposes process names/users to anyone on the VPN. | Functional spec §2.4 explicitly accepts: solo-operator threat model, VPN-gated. Out-of-band note for future shared-operator scenarios. |
| **htmx partial routing regressions** — splitting the single `/partial/dashboard` into six endpoints risks dropping cards. | Implementation strategy: keep `/partial/dashboard` as a thin alias of `/partial/overview` during the slice that introduces tabs; remove only after the new path is verified live. |
| **SQLite size with 7-day retention** at higher peer counts (say 20 clients) ≈ 170 MB. | Current count is 1; 8 days × 2880 samples × 20 peers = ~460 k rows. Even then the budget stays small. Add a comment in `poller.go` noting the worst-case formula. |
| **Chart re-render on theme toggle** could flicker. | `charts.js` rebuilds in place; Chart.js supports `update()` for color changes without destroy/recreate. Use `update()` first, only fall back to recreate if a color token can't be patched live. |
| **`/proc/[pid]/cmdline` truncation** — long command lines cut at the 60-char display cap could hide the differentiator. | Cap at 60 chars; show a `<span title="full cmdline">…</span>` tooltip on hover. |
| **WG interface stats parse drift** if `/proc/net/dev` format changes across kernels. | Pin parser to the Ubuntu 24.04 format. Add a unit test with a known good fixture. |
| **Tab + range URL hash collisions** if user shares a URL across dashboard versions. | Unrecognised hash values fall back to the default Overview tab + 24h range silently. No error UI. |

---

## 4. Testing Strategy

### Unit (Go)

| Package | Coverage |
|---|---|
| `disk` | Synthetic `/proc/mounts` fixture + Statfs-mockable interface. Asserts tmpfs/overlay exclusion + percentage math + amber/red thresholds. |
| `processes` | Synthetic `/proc` tree fixture (a temp dir with `stat`, `status`, `cmdline`). Two snapshots → asserts CPU% delta + top-5 ordering. |
| `netdev` | Static `/proc/net/dev` fixture for `wg0` + asserts each parsed field. |
| `geoip` | Embedded test DB (smaller fixture or the same Lite DB) + a handful of IPs incl. RFC1918, IPv6, public US/EU. |
| `db` | New test for `QueryClientTraffic(pubkey, from, to)`. Existing tests unchanged after retention bump (set `Retention` explicitly in test wiring). |
| `server` | Table-driven tests on each new `GET /partial/<tab>` endpoint: status 200, expected card markers in body, `?range=` enum validation returns 400 on bad values. |

### Integration / end-to-end

- Smoke run locally via `make run` (LISTEN_ADDR=`127.0.0.1:8080`): browse to each tab, confirm rendering with a seeded SQLite DB.
- After CI deploy to EC2, on a VPN client:
  - `curl http://172.16.15.1:8080/api/health` → `{"ok":true}`
  - `curl -sf http://172.16.15.1:8080/partial/system?range=7d | grep -q "Top 5 processes"` per tab
  - Browser visit: tab persistence (refresh on `#system?range=6h`), theme toggle persistence (refresh keeps choice).

### Manual / device

- Browser devtools at 360 px and 768 px viewports: tab pill bar scrolls horizontally with no page scroll; per-client expand stacks vertically.
- Dark-mode visual pass on both phone-default and laptop-default themes.

### Regression guard

- `make pre-commit` from repo root — fmt, docs, tflint, trivy still green (no Terraform-side changes, but the check is cheap).
- The four 002-era acceptance criteria that move into Overview / System / Network tabs must still pass (server endpoint info, service status, uptime, current CPU/mem/network). Asserted via the new partial-endpoint table-driven tests.
