<!--
This document describes HOW to build the feature at an architectural level.
It is NOT a copy-paste implementation guide.
-->

# Technical Specification: First-Client Onboarding & Dashboard Usability Fixes

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Four mostly-independent changes across two surfaces. **`scripts/install.sh`** gains (a) an example-client-config block in its success output and (b) an install / update / remove / purge lifecycle with a safe, no-clobber update path. The **Go dashboard** gets (c) inline full-width client editing (template + CSS, no endpoint change) and (d) a handshakes panel that resolves peer names at render time from the live client DB and shows one row per peer. No new tables, no new external dependencies, no architecture changes. Affected: `scripts/install.sh`, `dashboard/web/templates/{tabs/clients.html,cards/events.html}`, `dashboard/web/static/app.css`, `dashboard/internal/db/db.go`, `dashboard/internal/server/handlers_partial_tabs.go`.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Example client config in install output (`install.sh`)

- New helper `emit_example_client_config()` called from the success summary on **both** the WG-only and dashboard install paths, after the existing "WireGuard server is up" block.
- Inputs (all already available in the script): `SERVER_PUBLIC_KEY`, `WG_SERVER_PORT`, and a first-client IP derived from `WG_SERVER_NET` (server host + 1 → `/32`; default `172.16.15.1/24` → `172.16.15.2/32`). Document the "server is the first host" assumption.
- Emits a `wg-quick` `[Interface]`/`[Peer]` template:
  - `[Interface]`: `PrivateKey = <placeholder>`, `Address = <first-ip>/32`, `DNS = ${WG_CLIENT_DNS}`
  - `[Peer]`: `PublicKey = ${SERVER_PUBLIC_KEY}`, `Endpoint = <server-public-ip>:${WG_SERVER_PORT}`, `AllowedIPs = 0.0.0.0/0, ::/0` (note the split-tunnel alternative inline), `PersistentKeepalive = 25`
  - A one-line hint: generate the keypair off-host (`wg genkey | tee privatekey | wg pubkey`) and register the public key (dashboard or `WG_PEERS`).
- **No keypair is generated and no private key is ever created or stored on the server** — this is an illustrative template only.

### 2.2 Inline client editing (`tabs/clients.html`, `app.css`)

- Replace the `<details class="client-edit">` popover in the **Manage** cell with an **Edit toggle that reveals a full-width edit row** — a sibling `<tr class="client-edit-row hidden">` spanning all columns (`colspan`), mirroring the existing `detail-row hidden` toggle pattern used for the per-client stats panel.
- The existing edit `<form>` (same fields: name / public key / tunnel IP / note; same `hx-patch="/api/clients/{name}"`, `hx-target="#clients"`, `hx-swap="outerHTML"`) moves into that full-width row.
- CSS (`web/static/app.css`): drop the absolutely-positioned drawer rules for the old popover; lay the edit fields out across the row width (responsive flex/grid), readable on phone→ultrawide.
- **No handler/endpoint change.** PATCH `/api/clients/{name}` and the `#clients` fragment re-render on save are unchanged. The enable/disable/remove controls and the geo-map-outside-the-swap pattern are unaffected.

### 2.3 Handshakes: render-time names + one row per peer

- **Data Model:** no schema change. The `handshake_events` table keeps `ts, public_key, name`; the stored `name` becomes display-irrelevant (resolution moves to render time).
- **DB (`internal/db/db.go`):** add `QueryLatestHandshakePerPeer(ctx, from, to, limit)` returning one `HandshakeEvent` per `public_key` (`MAX(ts)` within the window), ordered newest-first.
- **Handler (`internal/server/handlers_partial_tabs.go`, events handler):** build a `public_key → name` map from `clientsSvc.List(ctx)` (already a `server` dependency); resolve each row's display name to the client name, or a shortened key + an "unknown" marker when the key is not a current client. The events view-model row carries `{TS, Name, Unknown}`.
- **Template (`cards/events.html`, `{{define "events"}}`):** render the resolved name (structure unchanged; optional styling for the unknown case). De-duplication and ordering come from the query, so the repeated-peer rows disappear.

### 2.4 Install / update / remove lifecycle (`install.sh`)

- **Argument parsing** (top of the script, before any work): `--uninstall`, `--purge` (⇒ uninstall **and** wipe data), `--dashboard-only` (uninstall modifier). No args ⇒ install/update; an unknown flag ⇒ usage error and non-zero exit. Resolves to an `ACTION` (`install`|`remove`) plus `PURGE`/`DASHBOARD_ONLY` booleans. The EC2 user-data wrapper's no-arg `bash install.sh` invocation is unchanged (install/update path).
- **Safe update (no clobber)** — in the wg0.conf step:
  - If `$WG_CONF` **exists** ⇒ *update*: render the new `[Interface]` block, **preserve the on-disk `[Peer]` stanzas** (extract from the first `[Peer]` line via `awk`, the same merge the `wg-sync` helper performs), write `[Interface]` + preserved peers, and apply with `wg syncconf wg0 <(wg-quick strip wg0)` so active tunnels are not dropped.
  - If `$WG_CONF` **absent** ⇒ *fresh install*: write `[Interface]` + `WG_PEERS` and `systemctl enable --now wg-quick@wg0`, as today.
  - Server key reuse is already handled (env → persisted `server.key` → generate).
- **Dashboard binary update:** already covered by the shipped `systemctl enable --now` → `enable` + `restart` change, so a rerun swaps the running process.
- **`remove()`** — stop + disable services and remove artifacts; idempotent (existence guards / `|| true`), ends with `systemctl daemon-reload`:
  - Dashboard (always): `wireguard-dashboard.service` (disable --now), its unit, `/usr/local/sbin/wg-sync`, the sudoers file, `/opt/wireguard-dashboard`, and the `wireguard-dashboard` user.
  - Unless `--dashboard-only`: also `wg-quick@wg0.service` (disable --now).
  - **Keep data by default** — `/etc/wireguard` (server key + `wg0.conf`) and `/var/lib/wireguard-dashboard` (client DB) are retained so a reinstall keeps the same server identity.
  - `--purge`: additionally delete `wg0.conf`, `server.key`, the dashboard DB dir, and `/etc/wireguard-dashboard` (clients.json / alerts.env).
- The script stays `set -euo pipefail` and shellcheck-clean across all actions.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** 2.3 reuses the existing `clientsSvc` on `server` (no schema change); 2.2 is pure frontend (no endpoint change); 2.1 and 2.4 are install-path only. The `wg-sync` merge logic is the reference for the 2.4 update merge.
- **Potential Risks & Mitigations:**
  - *Install-path regression (highest)* — the update branch rewrites the proven boot path. Mitigations: fresh-vs-update is keyed on `wg0.conf` existence; the peer-preserving merge mirrors the already-tested `wg-sync` helper; `wg syncconf` applies changes without dropping tunnels; `remove`/`--purge` are idempotent and guarded; shellcheck gates every action; **owner-run E2E** is required (live wg/systemd cannot run in-session).
  - *EC2 wrapper* — new arg parsing must not break the no-arg invocation; the owner verifies the rendered user-data still calls `bash install.sh` with no args (install/update).
  - *Handshakes resolution cost* — a `clientsSvc.List` per events render; N is small, negligible. Historical rows now display current names (acceptable / desirable).
  - *Inline edit htmx* — the `#clients` `outerHTML` swap and the geo-map-outside-the-swap invariant must keep working; covered by handler tests and the existing fragment idiom.

---

## 4. Testing Strategy

- **Go (in-session):**
  - `internal/db`: test `QueryLatestHandshakePerPeer` for one-row-per-peer dedupe + newest-first ordering against `db.Open(ctx, ":memory:")`.
  - `internal/server`: events-handler test for name resolution from a seeded clients DB (match → client name; unmatched key → shortened "unknown"); a handler/template test that the inline edit row renders the form with the correct `hx-patch` target.
  - Full `go build ./...` + `go vet ./...` + `go test ./...` + static `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` build green.
- **install.sh (in-session):** `make shellcheck` clean for the default, `--uninstall`, `--purge`, and `--dashboard-only` paths.
- **Owner-run E2E (cannot run in-session — real Ubuntu VPS):** fresh install prints the example client config; add a client via the UI; rerun-update preserves peers, does not drop the tunnel, and runs the new binary; `--uninstall` removes services but keeps data; reinstall keeps the same server identity; `--purge` produces a clean slate; `--dashboard-only` leaves the VPN running. EC2: owner confirms the rendered user-data path is behavior-unchanged.
- Per CLAUDE.md, live wg / systemd / VPS behavior is owner-verified, never claimed from a passing build.
