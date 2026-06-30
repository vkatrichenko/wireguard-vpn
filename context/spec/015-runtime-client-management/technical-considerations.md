# Technical Specification: Runtime Client Management

- **Functional Specification:** `context/spec/015-runtime-client-management/functional-spec.md`
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Make the dashboard's SQLite DB the runtime source of truth for peers, with Terraform's `clients_config` demoted to a first-boot seed. The dashboard gains write endpoints (mirroring the existing `POST /api/webhook*` pattern) to add/edit/remove clients. On every change the dashboard re-renders the full `/etc/wireguard/wg0.conf` to a staging file it owns, then invokes a **single new root helper** (`/usr/local/sbin/wg-sync`, one exact-match sudoers entry) that installs the file `0600` and runs `wg syncconf` — applying the peer diff live, with no interface bounce and no instance replacement. The same render-and-sync runs idempotently at dashboard startup (after seeding the DB from the Terraform-rendered `clients.json` if the table is empty), so a fresh VPS or a replaced EC2 instance converges to the seed. An export endpoint + a drift badge keep git reconcilable. Affected systems: the Go dashboard (`dashboard/`), `scripts/install.sh` (helper, sudoers, new env var, service), and the Terraform `wireguard` module (pass the subnet to the dashboard unit).

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Architecture Changes

- **Source of truth flips to the DB.** Today `buildClientRows` joins `clientsfile` (the Terraform-rendered `clients.json`) with live `wg show`. The client *list* now comes from a new `clients` DB service; `clients.json` is retained **only** as the immutable Terraform-seed snapshot used for (a) first-boot seeding and (b) drift comparison. The config-download path (`handlers_config.go`) switches its name lookup from `clientsfile.ByName` to the DB.
- **New live-apply path.** A new `internal/wgsync` service renders `wg0.conf` from the DB into a dashboard-owned staging file (`/var/lib/wireguard-dashboard/wg0.conf.staged`) and shells `sudo /usr/local/sbin/wg-sync` (same `Runner` seam idiom as `internal/wg`). The helper is the *only* privileged step.
- **Startup reconcile + seed.** On boot the dashboard: if `clients` table empty → import `clients.json`; then render + `wg-sync` (idempotent). Guarded so a dashboard-less deploy is unaffected (install.sh still writes the seed `wg0.conf` from `WG_PEERS` as today).

### Data Model / Database Changes

New table appended to the `const schema` block in `dashboard/internal/db/db.go` (additive, `CREATE TABLE IF NOT EXISTS`):

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER | PK AUTOINCREMENT (mutable entity → surrogate key) |
| `name` | TEXT | `UNIQUE`; wg/filename-safe charset |
| `public_key` | TEXT | `UNIQUE`; 44-char base64 |
| `address` | TEXT | `UNIQUE`; `x.x.x.x/32` |
| `enabled` | INTEGER | 0/1; disabled = retained but omitted from `wg0.conf` |
| `note` | TEXT | free-text, nullable |
| `created_at` / `updated_at` | INTEGER | unix-seconds (matches existing convention) |

New typed `Client` struct + methods in `db` (`ListClients`, `InsertClient`, `UpdateClient`, `DeleteClient`, `CountClients`) following the existing `const q` + `?`-placeholder style. Note: distinct from the existing `client_traffic` metrics table.

### API Contracts

Mirror the webhook dual-path convention (`isHTMX(r)` → HTML fragment re-render; else JSON; 200 on the htmx path even for validation failures so the fragment swaps with an inline error):

| Method / Path | Purpose | Body |
|---|---|---|
| `POST /api/clients` | Add a client | `name`, `public_key`, optional `address` (form or JSON) |
| `PATCH /api/clients/{name}` | Edit: rename, note, pubkey, address, enable/disable | changed fields |
| `DELETE /api/clients/{name}` | Remove a client | — |
| `GET /api/clients/export` | Export current clients | `?format=hcl` (default, `text/plain` attachment) or `tfvars` |

htmx issues `hx-patch`/`hx-delete` directly (Go 1.22 ServeMux matches the methods). Drift is folded into the Clients-tab view-model (and the page-data badge), not a separate endpoint. Existing `GET /api/clients`, `/partial/clients`, `/api/clients/{name}/config` keep their paths but read from the DB.

### Component Breakdown

- **`dashboard/internal/db`** — `clients` table + CRUD (above).
- **`dashboard/internal/clients`** (new) — orchestration: validation, IP allocation, CRUD-then-apply under a write mutex (serializes staging-file + `wg-sync`).
- **`dashboard/internal/wgsync`** (new) — render `wg0.conf` from clients + `Runner`-seam call to `sudo /usr/local/sbin/wg-sync`.
- **`dashboard/internal/wgconfig`** — add a pure `BuildServerConf`/`BuildServerPeer` (strings.Builder, mirrors existing `Build`) for the `[Peer]` stanzas; `[Interface]` block templated from server facts.
- **`dashboard/internal/server`** — new handlers (`handlers_clients_admin.go`), route registration, new struct deps appended to `New()` (per the append-only rule); switch client-list/config sources to the DB; add drift to page data.
- **`dashboard/web/templates`** — Clients-tab add/edit/remove forms (htmx fragments that keep their own `id` for `outerHTML` swaps, like `webhook.html`), an export button, and a drift badge.
- **`dashboard/cmd/wireguard-dashboard/main.go`** — wire new services; read new `WG_SERVER_NET` env (fallback to the existing `wgconfig` const); run startup seed+reconcile.
- **`scripts/install.sh`** — install `/usr/local/sbin/wg-sync` (0755 root:root); add one sudoers line `wireguard-dashboard ALL=(root) NOPASSWD: /usr/local/sbin/wg-sync` (inside the existing `visudo -c` validated block); pass `WG_SERVER_NET` into the dashboard unit (`Environment=`); ensure the dashboard user can write the staging file under `/var/lib/wireguard-dashboard`.
- **Terraform `modules/wireguard`** — no change to `clients_config` semantics (still renders `peers` + `clients.json` seed); ensure `wg_server_net` reaches the dashboard env via user-data (it already exports `WG_SERVER_NET`).

### Logic / Algorithm

- **IP allocation:** parse `WG_SERVER_NET` (e.g. `172.16.15.1/24`) → host range minus the server IP (`.1`) and any reserved; pick the lowest `/32` not present in the `clients` table. Manual override validated for in-subnet + uniqueness.
- **Validation:** public key = 44-char base64 (regex), address = `x.x.x.x/32` in-subnet, name charset + uniqueness, pubkey/address uniqueness. Reject atomically (no partial write).
- **Render + apply:** build full `wg0.conf` from server facts + enabled clients → write staging file → `sudo /usr/local/sbin/wg-sync`. Helper: `visudo`-safe, validates staging path, `install -m600 -o root -g root` to `/etc/wireguard/wg0.conf`, then `wg syncconf wg0 <(wg-quick strip wg0)`.
- **Seed + drift:** seed when table empty (import `clients.json`); drift = DB clients whose `public_key` is absent from the boot `clients.json` snapshot → count shown in badge.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** new sudoers entry + helper (install.sh/Terraform user-data, owner-approval gate); `wg syncconf`/`wg-quick strip` must exist on the host (they ship with wireguard-tools); the dashboard now needs `WG_SERVER_NET` (graceful fallback to the const).
- **Risks & Mitigations:**
  - *Privileged write path* → single tiny auditable helper, one exact-match (no-wildcard) sudoers line, staging-file validation, `install -m600`. Keeps the "read-only by design" posture minimally widened.
  - *Bad render bricking the tunnel* → `wg syncconf` only diffs peers (never bounces the interface); helper validates before install; render is unit-tested pure code.
  - *Concurrent edits racing the staging file* → single write mutex in `internal/clients`; DB already `SetMaxOpenConns(1)`.
  - *DB/`wg0.conf` divergence after a crash* → idempotent startup reconcile re-renders from the DB.
  - *EC2 rebuild loses runtime-only clients* → accepted; mitigated by export + drift badge (functional spec §3).
  - *Two SQLite openers (dashboard + any root path)* → avoided by keeping all DB access in the dashboard process; the helper consumes the rendered staging file, not the DB.

---

## 4. Testing Strategy

- **Unit (pure):** IP allocator (full/fragmented subnets, server-IP reserved, exhaustion), validators, `wgconfig` server-conf/peer renderer (enabled-only, disabled omitted, deterministic ordering), drift computation. Table-driven.
- **DB:** `db.Open(ctx, ":memory:")` (existing `newTestDB` helper) — CRUD, uniqueness-constraint violations, `CountClients`, seed-from-empty.
- **Handlers:** `httptest` + dep fakes (existing `server_test` harness) for add/edit/delete/export over both htmx (form-encoded) and JSON paths, asserting status/fragment/error behavior; a **fake `wgsync` applier** seam (no real `wg`/`sudo`/filesystem), asserting it's invoked with the expected rendered config on success and not on validation failure.
- **Manual / E2E (owner, post-deploy):** add a client in the UI → peer connects with no drop to others; remove → cut off; reboot → persists; export → valid HCL; drift badge reflects an un-exported client. Per CLAUDE.md, live tunnel behavior is owner-verified on the instance, not claimed from a passing build.
