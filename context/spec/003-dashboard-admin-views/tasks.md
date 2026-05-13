# Tasks: Web Dashboard v2 — admin views, tabs, longer time range

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Technical Specification:** [`technical-considerations.md`](./technical-considerations.md)

> Each slice leaves the dashboard runnable on the EC2 with new verifiable value. No Terraform / IAM / cloud-init changes are needed — the deploy chain from 002 carries the new binary every time.
>
> **Verification harness:** `curl http://172.16.15.1:8080/...` (over the WG tunnel, from the operator's laptop). Browser checks where the slice is visual. The CI deploy from 002 is what gets the binary onto the host; do not push to `main` to trigger it until each slice has been validated locally with `make run` (`LISTEN_ADDR=127.0.0.1:8080`) and an in-memory or seeded SQLite.

Vertical slices — each leaves the dashboard runnable with new verifiable value.

---

## Slice 1: Tab shell with Overview as default

**Outcome:** Page renders with six tab pills at the top. Overview tab shows the existing v3 single-page content. Other tabs render a "Coming soon" placeholder via htmx swap. URL fragment routing (`#overview`, `#clients`, …) drives selection; refresh lands on the same tab. Existing v3 endpoints remain functional.

- [x] Add `web/static/tabs.js` (~30 LOC) — on `DOMContentLoaded` and `hashchange`, parse `#tabname?range=…&expand=…`, mark the matching pill active, issue an htmx swap for the matching tab body. Unknown hash silently falls back to `#overview`. **[Agent: go-fullstack]**
- [x] Refactor `web/templates/dashboard.html` — keep header + stale pill, add a tab pill bar (`Overview / Clients / System / Network / Events / About`), replace single `<main id="dashboard-content">` with `<main id="tab-body">`. Initial server-side render dispatches the Overview body. Charts grid moves inside the System/Network tab partials (Slice 6 / Slice 8 finishes that move); for Slice 1 keep the existing chart grid below the tab body so v3 visuals don't regress. **[Agent: go-fullstack]**
- [x] Create `web/templates/tabs/overview.html` — references the existing v3 cards via `{{ template "..." }}` so this slice is a layout refactor with zero card edits. **[Agent: go-fullstack]**
- [x] Create five placeholder partials `web/templates/tabs/{clients,system,network,events,about}.html` — each just renders `<section class="card empty"><p class="empty">Coming soon.</p></section>`. **[Agent: go-fullstack]** _(template names: `clients`, `system-tab`, `network-tab`, `events-tab`, `about` — `-tab` suffix used where the card layer already owns the bare name)_
- [x] In `internal/server/`, add `handlers_partial_tabs.go` registering `GET /partial/overview`, `/partial/clients`, `/partial/system`, `/partial/network`, `/partial/events`, `/partial/about`. Each returns its tab body fragment. Keep `/partial/dashboard` as a thin alias of `/partial/overview` for one release. **[Agent: go-fullstack]** _(alias implemented by routing both `/partial/dashboard` and `/partial/overview` to `handleGetPartialOverview`; old `handlers_partial.go` deleted since the only handler moved)_
- [x] Extend `web/static/app.css` — tab pill bar styles, active-pill state, horizontal scroll on viewports <600 px, 44 px touch targets. **[Agent: go-fullstack]**
- [x] Add `internal/server/server_test.go` cases: `GET /partial/<each-tab>` returns 200, body contains the expected sentinel string (`"Coming soon"` for placeholders, the existing v3 card markers for overview). **[Agent: go-fullstack]**
- [x] Verify locally: `make run`, browse to `http://127.0.0.1:8080`, click each tab — body swaps, URL hash updates. Refresh on `#system` lands on System. **[Agent: go-fullstack]**

---

## Slice 2: Dark-mode toggle (theme tokens + chart re-render)

**Outcome:** Toggle button in the header flips light ↔ dark. First load matches `prefers-color-scheme`. Choice persists in `localStorage`. All four existing 24h charts honor the active theme.

- [x] Refactor `web/static/app.css` to define color tokens on `:root` (background, surface, text, muted, accent, danger, success, gridline) and override them under `:root[data-theme="dark"]`. Every existing rule that hard-codes a color migrates to a `var(--token)` reference. **[Agent: go-fullstack]**
- [x] Add `web/static/theme.js` — on load: read `localStorage.theme`, fall back to `prefers-color-scheme`; set `<html data-theme="…">` accordingly. On toggle-button click: flip the attribute and persist. Expose `window.__themeChanged` event for charts.js to listen to. **[Agent: go-fullstack]**
- [x] Update `web/templates/dashboard.html` — add a toggle button in the header (`<button id="theme-toggle" aria-label="Toggle dark mode">`), include `theme.js` with `defer` before `charts.js`. **[Agent: go-fullstack]**
- [x] Update `web/static/charts.js` — read color tokens via `getComputedStyle(document.documentElement)` at chart-init time; listen for `__themeChanged` and call each chart's `update()` after patching colors. Recreate only as a fallback if `update()` can't take a color. **[Agent: go-fullstack]**
- [x] Verify locally: toggle in light viewport, refresh — stays dark. Charts gridlines + series colors visibly change. Hard-reload with `prefers-color-scheme: dark` (devtools emulation) and no localStorage entry — starts dark. **[Agent: go-fullstack]**

---

## Slice 3: SQLite retention bump + per-client query helper

**Outcome:** No visible UI change. Dashboard now retains ~8 days of samples; a per-public-key query method exists for Slice 5 to consume. Existing 24h charts continue to render exactly as before.

- [x] In `internal/db/db.go`, add `QueryClientTraffic(ctx, publicKey, from, to)` returning `[]ClientTraffic` for a single key. Reuse the existing `idx_client_traffic_ts` index path. **[Agent: go-fullstack]** _(existing all-keys variant renamed to `QueryClientTrafficAll`; per-key takes the bare name)_
- [x] Add a unit test in `internal/db/db_test.go` — seed two peers' rows, assert the new helper returns only the requested key's rows in `[from, to]` and in ts-ascending order. **[Agent: go-fullstack]**
- [x] In `internal/poller/poller.go`, change `DefaultRetention = 25 * time.Hour` to `DefaultRetention = 8*24*time.Hour + time.Hour`. Update the surrounding comment block to record the rationale (7-day chart window + 1 h slack, expected DB ≈ 17 MB at 2 peers / 30 s cadence). **[Agent: go-fullstack]**
- [x] Verify locally: run `make test` — existing prune sweep test continues to pass (cutoff arithmetic is parameterised in tests, not hard-coded to 25 h). Run `make run` with a seeded DB containing rows older than 25 h but younger than 8 d — they survive the first prune. **[Agent: go-fullstack]**

---

## Slice 4: Clients tab — full table with offline geolocation

**Outcome:** Clients tab renders the full client table (name, WG IP, online/offline, last-handshake, cumulative rx/tx, peer public-IP endpoint, geolocation). No per-row expand yet — that's Slice 5.

- [x] Vendor `internal/geoip/GeoLite2-City.mmdb` (download from MaxMind; commit alongside `LICENSE-GeoLite2.txt` with CC BY-SA 4.0 attribution). Add an `internal/geoip/README.md` documenting the refresh procedure (replace file, rebuild). **[Agent: go-fullstack]** _(actual size 63 MB, not the ~7 MB the spec originally estimated — that was Country, not City; binary grows accordingly. Refresh via `geoipupdate` per the README.)_
- [x] Add `github.com/oschwald/geoip2-golang` to `go.mod` (`go get` at the current released version). Confirm CGO_ENABLED=0 build still works locally. **[Agent: go-fullstack]** _(v1.13.0 + transitive maxminddb-golang v1.13.0; marked `// indirect` until sub-task 3 imports them)_
- [x] New package `internal/geoip/geoip.go` — load the `.mmdb` via `embed.FS` at startup; expose `Lookup(ip net.IP) (country, city string)` returning empty strings for unresolvable / RFC1918 / IPv6 link-local. Singleton initialised once in `cmd/wireguard-dashboard/main.go`. **[Agent: go-fullstack]**
- [x] Unit test in `internal/geoip/geoip_test.go` — table-driven cases: known US public IP, known EU public IP, RFC1918 10.x, RFC1918 192.168.x, IPv6 fe80::, malformed input. **[Agent: go-fullstack]**
- [x] Extend `internal/server/handlers_clients.go` (or its successor) — build per-row `clientRow` that includes the resolved geo, sourced from the peer's `endpoint` column already returned by `wg show wg0 dump`. **[Agent: go-fullstack]** _(also updated `handlers_snapshot.go` — second `buildClientRows` call site the spec didn't list; mechanical `nil` / `s.geoipSvc` arg add. Added `Geo` struct + `GeoResolver` interface in `clientrows.go`; `server.New(...)` now takes `*geoip.Service` as trailing arg.)_
- [x] Replace `web/templates/tabs/clients.html` placeholder with the real table. Columns: name, WG IP, online (pill), last-handshake, rx, tx, endpoint, geo. Empty state matches v3 wording ("No clients configured. Add via `terraform/dev/main.tf`."). **[Agent: go-fullstack]**
- [x] Extend `internal/server/server_test.go` — `GET /partial/clients` returns 200 and includes the geo column header + a known peer's row. **[Agent: go-fullstack]**
- [x] Verify locally with a seeded SQLite: Clients tab renders all peers with country/city; RFC1918 endpoint shows "—". **[Agent: go-fullstack]**

---

## Slice 5: Clients tab — inline expand with per-client chart + p95

**Outcome:** Click a client row → inline expansion below it with a 24 h rx/tx chart and a p95 throughput figure over the active range. Only one row expanded at a time. `#clients?expand=<pubkey>` deep-links to a pre-expanded row.

- [x] New `internal/server/handlers_clients.go` (or extend) — `GET /partial/clients/{pubkey}/detail` returns the expand fragment (chart canvas wrapper + p95). 404 on unknown pubkey. **[Agent: go-fullstack]**
- [x] New `GET /api/metrics/client/{pubkey}?range=…` — JSON time-series `{ts, rx_rate_bps, tx_rate_bps}` from consecutive `QueryClientTraffic` rows. Range param validated to enum 1h/6h/24h/7d; default 24h. **[Agent: go-fullstack]**
- [x] New `internal/p95` helper (or inline in handler) — input `[]float64` rates, return p95 via nearest-rank. **[Agent: go-fullstack]**
- [x] New `web/templates/cards/client-detail.html` — chart canvas + "p95 over range: X B/s" + small range hint. **[Agent: go-fullstack]**
- [x] Update `web/templates/tabs/clients.html` rows — each row gets `hx-get="/partial/clients/{pubkey}/detail"`, `hx-target="#detail-{pubkey}"`, `hx-swap="innerHTML"`. Below each row, an empty `<tr id="detail-{pubkey}" class="detail-row hidden">` ready to fill. **[Agent: go-fullstack]**
- [x] Update `web/static/charts.js` — add `initClientChart(pubkey, range)` invoked after the detail fragment swaps in. Use the htmx `htmx:afterSwap` event. **[Agent: go-fullstack]**
- [x] Update `web/static/tabs.js` — on tab-body swap, if hash carries `?expand=<key>`, trigger the row's `hx-get` automatically. **[Agent: go-fullstack]**
- [x] CSS: `.detail-row` collapsed by default; one-row-expanded constraint enforced via JS (`tabs.js` collapses any other open detail before opening the new one). **[Agent: go-fullstack]**
- [x] Unit test: `GET /api/metrics/client/<known>` returns rates with monotonic ts; `GET /api/metrics/client/<known>?range=99x` returns 400. **[Agent: go-fullstack]**
- [x] Verify locally + on EC2 after the next CI deploy: click a row, chart renders, p95 figure displays. Open `#clients?expand=<pubkey>` directly, that row is pre-expanded. **[Agent: go-fullstack]**

---

## Slice 6: System tab — disk usage card

**Outcome:** System tab is no longer a "coming soon" placeholder: it shows current CPU/mem (existing) plus the new disk usage card. Time-range selector arrives in Slice 9; until then System defaults to 24 h.

- [x] New package `internal/disk/disk.go` — parse `/proc/mounts`; for each non-pseudo filesystem (skip `tmpfs`, `devtmpfs`, `overlay`, `squashfs`, `proc`, `sysfs`, `cgroup*`, `debugfs`, `tracefs`), call `unix.Statfs` and compute `{Path, FsType, Used, Total, PctFull}`. **[Agent: go-fullstack]**
- [x] Unit test in `internal/disk/disk_test.go` — synthesise a `/proc/mounts` fixture file, mock the Statfs call via an injected function, assert filter + math + threshold helpers. **[Agent: go-fullstack]**
- [x] New `web/templates/cards/disk.html` — rows per mount with mount path, used / total (human-formatted via existing `humanBytes`), percentage-full progress bar. Bar class amber ≥80 %, red ≥95 %. **[Agent: go-fullstack]**
- [x] Promote `web/templates/tabs/system.html` from placeholder — embeds existing CPU/mem large-numeric cards + the new disk card. Reuse existing CPU/mem 24 h charts inside the tab body (move them out of the global `<section class="charts-grid">`). **[Agent: go-fullstack]**
- [x] CSS for `.progress-bar` (foreground/background tokens, transition on percentage width). **[Agent: go-fullstack]**
- [x] Server-side test: `GET /partial/system` returns 200; body contains `"Top mounts"` (or whatever the disk card heading is) and the percentage bar markup. **[Agent: go-fullstack]**
- [x] Verify locally: System tab renders disk rows for `/` and any other mounts. Threshold colors verified by editing the test fixture to force a >95 % row. **[Agent: go-fullstack]**

---

## Slice 7: System tab — top-5 processes by CPU%

**Outcome:** System tab now also shows a top-5 process table that updates each 10 s tick.

- [x] New package `internal/processes/processes.go` — `Service` struct (singleton, mutex-guarded prior snapshot), `Sample(ctx)` reads `/proc/[pid]/stat`, `/proc/[pid]/status`, `/proc/[pid]/cmdline` for every PID, computes per-PID CPU% via `(utime + stime)` delta against `/proc/stat` total-jiffies delta, returns top-5 sorted by CPU%. ENOENT during walk → skip (PID exited race). **[Agent: go-fullstack]**
- [x] Unit test — populate a `TempDir`-rooted synthetic `/proc` tree at two snapshots; assert delta math + top-5 ordering + ENOENT tolerance. Inject the proc root path via constructor argument. **[Agent: go-fullstack]**
- [x] Wire `processes.New(...)` singleton in `cmd/wireguard-dashboard/main.go` alongside `proc.Service`. Add a warm-sample on startup so the first request has non-zero deltas. **[Agent: go-fullstack]**
- [x] New `web/templates/cards/processes.html` — table: PID, user, CPU%, mem%, command (truncated to 60 chars, `title=` tooltip with full cmdline). **[Agent: go-fullstack]**
- [x] Wire the processes card into `web/templates/tabs/system.html`. **[Agent: go-fullstack]**
- [x] Server-side test: `GET /partial/system` body contains a PID row sentinel. **[Agent: go-fullstack]**
- [ ] Verify locally: top-5 visibly re-orders as you hit the page with `stress --cpu 2` running in another terminal. **[Agent: go-fullstack]**

---

## Slice 8: Network tab — WireGuard interface stats + aggregate traffic

**Outcome:** Network tab is no longer a placeholder: it shows current rx/tx (existing), the rx/tx 24 h charts (moved from the global grid), the new WG interface stats card, and the new aggregate-traffic-over-range card.

- [ ] New package `internal/netdev/netdev.go` — parse `/proc/net/dev` for the `wg0` row, return `{Peers, RxBytes, TxBytes, RxPackets, TxPackets, RxErrs, TxErrs, RxDropped, TxDropped}`. Peer count comes from `wg show wg0 dump` (`wg.go` already shells out; reuse the existing helper). **[Agent: go-fullstack]**
- [ ] Unit test — `/proc/net/dev` fixture for a known good wg0 row; assert each field. **[Agent: go-fullstack]**
- [ ] New `web/templates/cards/wg-iface-stats.html` — single card with the eight fields + the peer count. **[Agent: go-fullstack]**
- [ ] New `web/templates/cards/aggregate-traffic.html` — "Last 24h: A in / B out" using rx/tx cumulative deltas from `traffic_metrics` between (now-range, now). **[Agent: go-fullstack]**
- [ ] Promote `web/templates/tabs/network.html` from placeholder — current rx/tx large numerics + 24 h rx/tx charts (moved out of global grid) + WG iface stats + aggregate-traffic. **[Agent: go-fullstack]**
- [ ] Server-side test: `GET /partial/network` body contains the WG iface stats card heading + an "in / out" sentinel. **[Agent: go-fullstack]**
- [ ] Verify locally: Network tab shows the four cards. Aggregate matches `wg show wg0 dump` rx/tx deltas over the last 24 h. **[Agent: go-fullstack]**

---

## Slice 9: Time-range selector (1h / 6h / 24h / 7d) on System + Network

**Outcome:** Each chart-bearing tab gets a single range selector at the top. Selection persists in the URL fragment alongside the tab name. 7d shows whatever data has accumulated since the retention bump in Slice 3.

- [ ] Extend the chart JSON endpoints — `GET /api/metrics/system?range=…` and `GET /api/metrics/traffic?range=…` accept the four-value enum, default 24h, return 400 on anything else. The range maps to `now - duration` for the `from` timestamp. **[Agent: go-fullstack]**
- [ ] Propagate `?range=` through the tab partial endpoints — `GET /partial/system?range=…` and `GET /partial/network?range=…` render the selector with the current value pre-selected and inject the range into chart bootstrap data attributes. **[Agent: go-fullstack]**
- [ ] Extend `web/static/charts.js` — read the `data-range` attribute on chart elements; on `<select>` change, re-fetch the JSON endpoint with the new range and call `chart.update()`. **[Agent: go-fullstack]**
- [ ] Extend `web/static/tabs.js` — parse `?range=` from the URL hash and rewrite the htmx swap URL to include it; on selector change, update the hash via `pushState`. **[Agent: go-fullstack]**
- [ ] Update `cards/aggregate-traffic.html` and its handler so the "Last Xh" label and the numbers honor the active range. **[Agent: go-fullstack]**
- [ ] Server-side test: `GET /api/metrics/system?range=7d` returns up to 8 d worth of points; `?range=99x` returns 400. **[Agent: go-fullstack]**
- [ ] Verify locally with a DB seeded with >24 h of data: `#system?range=7d` shows the full 7-day curve; `#system?range=1h` zooms in. Refreshing the page preserves the range. **[Agent: go-fullstack]**

---

## Slice 10: Events tab — most recent 50 handshakes

**Outcome:** Events tab shows the 50 newest handshake events. Cap raised from 10 (Overview / v3 layout) to 50 because the dedicated tab has room.

- [ ] Extend `internal/db/db.go` — `QueryHandshakeEvents` already supports a `limit` arg; the Events tab handler just calls it with `50`. No schema change. **[Agent: go-fullstack]**
- [ ] Promote `web/templates/tabs/events.html` from placeholder — `{{ template "events" . }}` re-using the existing card with a 50-row dataset. Same empty state wording. **[Agent: go-fullstack]**
- [ ] Server-side test: `GET /partial/events` returns 200; body contains the events table heading or empty-state copy. **[Agent: go-fullstack]**
- [ ] Verify locally: with a seeded DB, the table shows up to 50 newest rows in descending ts order. **[Agent: go-fullstack]**

---

## Slice 11: About tab — EC2 + binary + OS metadata

**Outcome:** About tab is fully populated. Build SHA and timestamp are injected at build time via `-ldflags -X`.

- [ ] Extend `internal/serverinfo/serverinfo.go` — IMDSv2 reads for `instance-type`, `placement/availability-zone`, `ami-id` (public IP already done). Add a `Kernel()` reader (uname-like via `unix.Uname`) and `OSRelease()` reader for `/etc/os-release`. **[Agent: go-fullstack]**
- [ ] Build-time metadata — declare `var BuildSHA, BuildTime, GoVersion string` package-level in `cmd/wireguard-dashboard/main.go`; populate via `-ldflags "-X main.BuildSHA=… -X main.BuildTime=…"`. Update `dashboard/Makefile` `build` target to set the values from `git rev-parse --short HEAD` and `date -u +%FT%TZ`. Update the CI workflow `dashboard-build.yml` to set them from the workflow's SHA + run start time. **[Agent: cicd-github-actions]**
- [ ] New `web/templates/cards/about-ec2.html`, `about-binary.html`, `about-os.html`. **[Agent: go-fullstack]**
- [ ] Promote `web/templates/tabs/about.html` from placeholder — three cards. Include the existing server-public-key copy button as a fourth card (also stays on Overview's server-info). **[Agent: go-fullstack]**
- [ ] Server-side test: `GET /partial/about` body contains an "Instance type" sentinel and a "Build" sentinel. IMDSv2 reads need a mockable injection point to keep tests offline — same pattern as the existing serverinfo tests. **[Agent: go-fullstack]**
- [ ] Verify locally with mocked metadata; verify on EC2 after deploy that the real values render. **[Agent: go-fullstack]**

---

## Slice 12: Overview tab consolidation

**Outcome:** Overview is the compact "everything-OK?" view per §2.2 — no 24 h charts, no full client list, no event log. Adds the new client-count summary card.

- [ ] Edit `web/templates/tabs/overview.html` to compose only: server-info, service-status, uptime, **new** client-count summary, current CPU% / memory% large numerics, current rx/tx rate. **[Agent: go-fullstack]**
- [ ] New `web/templates/cards/client-count.html` — "**N online** / M total" with the existing online/offline color tokens. Handler computes from the same client snapshot used by the Clients tab. **[Agent: go-fullstack]**
- [ ] Remove the legacy `<section class="charts-grid">` from `dashboard.html` (charts now live under System / Network). **[Agent: go-fullstack]**
- [ ] Server-side test: `GET /partial/overview` body contains "N online" sentinel + the existing v3 server-info marker; does **not** contain the chart canvas IDs (those have moved). **[Agent: go-fullstack]**
- [ ] Verify locally: Overview matches §2.2 acceptance criteria exactly. **[Agent: go-fullstack]**

---

## Slice 13: Mobile responsiveness audit across all new content

**Outcome:** Every new tab respects 360 px / 600 px / 44 px-touch-target. Tab pill bar scrolls horizontally on narrow viewports without page scroll. Per-client expand row stacks vertically below 600 px.

- [ ] CSS audit pass on `app.css` across the new cards: disk progress bars, processes table, WG iface stats, aggregate-traffic, about cards, client-detail expand. **[Agent: go-fullstack]**
- [ ] Add explicit `overflow-x: auto` + `scroll-snap` on the tab pill bar; ensure each pill is ≥44 px touch target. **[Agent: go-fullstack]**
- [ ] Add a `@media (max-width: 600px)` block that re-flows multi-column cards (disk, processes, about) to single-column. **[Agent: go-fullstack]**
- [ ] Verify in browser devtools at 360 px and 768 px: every tab. No horizontal page scroll at 360. Pill bar scrolls horizontally. Tap targets pass. **[Agent: go-fullstack]**

---

## Slice 14: Drop the v3 single-page aliases + final cleanup

**Outcome:** Old endpoints and templates that are no longer reachable are removed. `make pre-commit` is clean.

- [ ] Remove `/partial/dashboard` alias from `handlers_partial.go` (or the file that owns it). Confirm no template or JS still references it. **[Agent: go-fullstack]**
- [ ] Delete `web/templates/dashboard-content.html` if it is no longer referenced after the Slice 1 refactor. **[Agent: go-fullstack]**
- [ ] Grep for orphan template names — any `{{ define "..." }}` block with zero callers gets removed. **[Agent: go-fullstack]**
- [ ] Update `internal/server/server_test.go` to drop the alias-coverage test added in Slice 1. **[Agent: go-fullstack]**
- [ ] Run `make test` and `make pre-commit` — green. **[Agent: devsecops-quality]**
- [ ] CI deploy from `main`; on a VPN client visit each tab from a phone and a laptop. Confirm Slice 1–13 acceptance criteria all hold on the deployed binary. **[Agent: go-fullstack]**

---

## Notes on agents

Every implementation sub-task in this spec is Go application work — backend handlers, `embed.FS`-bundled templates, vanilla JS, CSS, SQLite. The new `go-fullstack` agent (added via `/awos:hire` for this spec) owns all of it.

- `cicd-github-actions` is used in Slice 11 only for the `-ldflags -X` build-time injection in the CI workflow.
- `devsecops-quality` is used in Slice 14 for the final `make pre-commit` pass.
- No Terraform, IAM, cloud-init, or WireGuard-config changes are needed for this spec, so `terraform-aws`, `linux-cloud-init`, and `wireguard-networking` are not assigned anywhere.
