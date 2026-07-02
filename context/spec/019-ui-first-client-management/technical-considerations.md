<!--
Technical spec for the UI-first client-management refactor (spec 019).
Structures & contracts, not implementations.
-->

# Technical Specification: UI-First Client Management

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

This is predominantly **removal and reduction** of the spec-018 machinery, across three layers, with one small new script:

- **Terraform** drops the declarative `clients_config` list and the S3 drift `check`. It keeps a single `admin_peer` (name + public key) as the anti-lockout seed, and keeps the S3 **bucket + IAM** (no Terraform-managed seed object — the dashboard initializes it).
- **Dashboard (Go)** keeps the `internal/clientstore` S3 code and the PR #53/#54 boot-restore/write-through hardening, reframed as a **backup** (no drift, no `clients_config` comparison). The spec-015/016 **drift badge and HCL/tfvars export are removed** (their git baseline is gone).
- **Installer** always deploys the dashboard alongside WireGuard (no WG-only path) and installs a new on-box **`wg-peer`** script that is a thin client over the **local dashboard HTTP API** — so it is the same mutation path as the UI by construction.

Affected areas: `terraform/dev/*`, `terraform/modules/wireguard/*`, `scripts/install.sh`, a new `scripts/wg-peer` (or bundled), `dashboard/internal/{clients,clientstore,server}`, `dashboard/web/templates/*`.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Terraform — replace the peer list with one admin peer

- **Root (`terraform/dev/locals.tf`, `main.tf`):** remove `clients_config` (the list). Add `admin_peer = { name, public_key }` (single object, nullable). Pass `admin_peer` to the module.
- **Module input (`variables.tf`):** remove `clients_config`; add `admin_peer` object var with `default = null` (so a no-admin deploy is valid). Validate the public key shape when non-null.
- **Module (`locals.tf`):** render the `WG_PEERS` stanzas and the `CLIENTS_JSON` boot seed from the **single admin peer** (or empty when `admin_peer == null`). Remove `clients_by_address` and `clients_canonical_json`. Keep `client_store_s3_bucket`; set `client_store_s3_key` to the constant `"clients.json"`.
- **Module (`client_store.tf`):** **keep** `aws_s3_bucket.client_list` + versioning + public-access-block + SSE + `force_destroy`, and the instance-role `s3:GetObject`/`s3:PutObject` statement (resource ARN = the fixed string `"${aws_s3_bucket.client_list[0].arn}/clients.json"`). **Remove** `aws_s3_object.clients` — the dashboard creates/initializes the object on first boot via the existing 404-init path.
- **Module (`checks.tf`):** **delete** — no drift detection.
- **Module (`outputs.tf`):** keep `client_list_bucket` (informational); drop any `clients_config`-derived outputs.

### 2.2 Dashboard (Go + UI) — S3 as backup, remove drift UI

- **Keep** `internal/clientstore` (Load/Save via the `aws` CLI, `NoopStore`, canonical serializer) and `internal/clients/service.go`'s `ReconcileFromStore` (boot restore) + `saveStoreLocked` (write-through), **including all PR #53/#54 hardening** (empty ≠ authoritative, `storeReady` guard, best-effort write-through, lazy self-heal). The cold-seed/localSeed source is now the single admin peer's `clients.json` — same mechanism, one entry.
- **Remove the drift feature (spec 015/016):** the **drift badge** and the **HCL/tfvars export** in `web/templates/tabs/clients.html` (+ `cards/*`), the export handler/route, and the SQLite-vs-`clients.json`-baseline comparison logic. There is no declarative git list to reconcile against.
- No change to the live-apply path (`wg-sync` helper, peers-only file, `wg syncconf`) or the `clients` table schema.

### 2.3 New on-box `wg-peer` script (thin client over the local API)

- Installed by `install.sh` at `/usr/local/bin/wg-peer`. Talks to the **local dashboard API** at `LISTEN_ADDR`, reusing the exact spec-004/015 endpoints — so IP allocation, `wg syncconf`, S3 write-through, and config generation all run through the dashboard's Go code (script ≡ UI).
- **Command surface:**
  - `wg-peer add <name> [--pubkey KEY] [--show-config]` — without `--pubkey`, generate an **ephemeral** keypair (`wg genkey | wg pubkey`); `POST /api/clients {name, public_key}`. With `--show-config`, fetch the dashboard's generated client config (spec 004 download endpoint), substitute the generated **private** key, print to stdout, then **discard** it (never written to disk/SQLite/S3).
  - `wg-peer remove <name>` — `DELETE /api/clients/<name>`.
  - `wg-peer update <name> [--name NEW | --pubkey KEY]` — `PATCH /api/clients/<name>`.
- Depends on the dashboard being installed and running (guaranteed — §2.4). Reads the API base from the same env the unit uses.

### 2.4 Installer — dashboard always on; env

- **Dashboard is always deployed with WireGuard.** Remove the WG-only install path and the "dashboard disabled when `DASHBOARD_RELEASE_TAG` is empty" gating; `dashboard_release_tag` becomes **required / always-set** (Terraform default or validation). `--dashboard-only` (update just the dashboard binary) may remain as an update convenience.
- Thread `ADMIN_PEER_NAME` + `ADMIN_PEER_PUBLIC_KEY` (replacing the multi-peer list content of `WG_PEERS`/`CLIENTS_JSON` with the single admin peer; empty when no admin). Keep `CLIENT_MANAGEMENT_MODE`/`CLIENT_STORE_S3_*` and the PATH fix. Install the `wg-peer` script and its dependencies (`wg`, `curl`, `jq` if needed).

---

## 3. Impact and Risk Analysis

- **System dependencies:** relies on the existing dashboard API (specs 004/015), the `wg-sync` helper + sudoers, the `clientstore`/S3 code, and IMDSv2/`WG_PUBLIC_ENDPOINT` for config generation.
- **No instance churn on peer edits** — only the admin peer feeds `user_data`; day-to-day peer management is UI/script only. Editing the admin peer still rolls the launch template (rare, accepted).
- **Ephemeral server-side keygen** (script `--show-config`) is a *scoped* deviation from "the server never holds a client private key." Mitigation: generated → printed → discarded, never persisted; `--pubkey` path keeps the strict behavior. Document it.
- **Dashboard-always** narrows the installer's scope (no WG-only). Standalone-VPS installs now always include the dashboard; `dashboard_release_tag` can no longer be empty.
- **S3 backup** keeps last-writer-wins + versioning; no drift visibility (accepted — UI is the sole authority). Retains the fail-safe: empty/unreadable S3 never wipes the DB.
- **Migration:** clean re-deploy (test env); the currently-deployed v0.0.15 cloud state is replaced. No in-place migration tooling.

---

## 4. Testing Strategy

- **Go:** keep/adapt the `clientstore` + `ReconcileFromStore` fake-store tests (restore-from-backup, 404-init from the admin seed, non-404 no-clobber, write-through, self-heal). Remove the drift-check/baseline tests. `go build/vet/test ./...` + `gofmt` + static arm64.
- **Terraform:** `fmt -recursive` + `make pre-commit`. Confirm `local` provisions no S3; `cloud` creates the bucket + IAM but **no** seed object and **no** `check`; `admin_peer = null` is valid.
- **Script:** `make shellcheck`; a local test against a stub/localhost API — `add` registers the public key and (with `--show-config`) prints a well-formed config; `remove`/`update` hit the right endpoints; the ephemeral private key never lands on disk.
- **Owner E2E:** fresh AWS deploy → admin peer connects, dashboard reachable; UI add/remove applies live with **no instance replacement**; instance rebuild → peers restored from S3; `wg-peer add --show-config` on a manual VPS produces a usable config and the peer appears in the UI.
