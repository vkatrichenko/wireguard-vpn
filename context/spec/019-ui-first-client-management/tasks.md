# Tasks: UI-First Client Management

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)
- **Status:** In Progress

---

> **Design recap:** The dashboard UI (and the equivalent on-box `wg-peer` script, which drives the same local API) is the **only** peer-management authority. Terraform seeds a single `admin_peer` (name + public key) for anti-lockout and nothing more — no declarative `clients_config` list, no S3 drift `check`. `local` = SQLite only; `cloud` = SQLite + S3 as a pure backup (write-through + boot restore), keeping the PR #53/#54 hardening. The dashboard is always deployed with WireGuard.
>
> **Simplification:** the module already renders `WG_PEERS`/`CLIENTS_JSON` for the boot seed — it now renders those from the single `admin_peer` (one entry), so no new `ADMIN_PEER_*` env vars and minimal `install.sh` change.
>
> **Verification reality:** No live `terraform plan`/`apply` in-session (`csm` SSO expired — owner runs it). No browser MCP — UI verified via Go build/tests + owner E2E. No live dashboard API in-session — the `wg-peer` script is tested against a stub/localhost API. Live S3/EC2/VPS is owner-run (Slice 5).
>
> **Per-slice gate:** Terraform → `terraform fmt -recursive` + `make pre-commit`; `install.sh`/script → `make shellcheck`; dashboard → `cd dashboard && go build ./... && go vet ./... && go test ./...` + static arm64 build.

---

## Slice 1 — Terraform: `clients_config` list → single `admin_peer`; strip drift check + S3 seed (req 2.1, 2.2, 2.4)

- [x] Terraform manages exactly one `admin_peer` (name + public key), no declarative peer list, no drift `check`; S3 bucket/IAM kept as backup infra **[Agent: terraform-aws]**
  - [x] Root `terraform/dev/locals.tf` + `main.tf`: remove the `clients_config` list; add `admin_peer = { name, public_key }` (nullable); pass `admin_peer` to the module
  - [x] Module `variables.tf`: remove `clients_config`; add `admin_peer` object var (`default = null`; validate the public-key shape when non-null)
  - [x] Module `locals.tf`: render `WG_PEERS` stanzas + `CLIENTS_JSON` boot seed from `admin_peer` (one entry; empty when `null`); remove `clients_by_address` / `clients_canonical_json`; set `client_store_s3_key = "clients.json"`
  - [x] Module `client_store.tf`: remove `aws_s3_object.clients` (TF seed); keep `aws_s3_bucket.client_list` + versioning + public-access-block + SSE + `force_destroy` and the `s3:GetObject`/`s3:PutObject` IAM statement (resource ARN = fixed `"${aws_s3_bucket.client_list[0].arn}/clients.json"`)
  - [x] Delete module `checks.tf`; prune any `clients_config`-derived outputs (keep `client_list_bucket`)
  - [x] Make `dashboard_release_tag` required (validation / non-empty) — the dashboard is always deployed (Slice 3)
  - [x] Thread `admin_peer`-derived values through `locals.tf` templatefile map → `templates/user-data.txt` (no new env vars beyond the existing `WG_PEERS`/`CLIENTS_JSON`)
  - [x] Verify: `terraform fmt -recursive` + `make pre-commit` green; confirm `local` provisions no S3, `cloud` creates bucket + IAM but **no** seed object and **no** `check`, and `admin_peer = null` is valid

## Slice 2 — Dashboard: S3 backup-only; remove drift badge + export (req 2.1, 2.3, 2.4)

- [x] The dashboard treats S3 as a pure backup; the git-reconciliation UI (drift badge + HCL/tfvars export) is gone; the store hardening is retained **[Agent: go-fullstack]**
  - [x] Remove the drift badge + HCL/tfvars export from `web/templates/tabs/clients.html` (+ `cards/*`), the export handler/route, and the SQLite-vs-`clients.json`-baseline comparison logic
  - [x] Keep `internal/clientstore` + `internal/clients/service.go` `ReconcileFromStore` (boot restore) + `saveStoreLocked` (write-through) + all PR #53/#54 hardening (empty ≠ authoritative, `storeReady` guard, best-effort, self-heal); cold-seed/localSeed source is the one-peer `clients.json`
  - [x] Adapt tests: drop the drift-baseline/export tests; keep restore-from-backup, 404-init-from-seed, non-404 no-clobber, write-through, self-heal (fake store, no live AWS)
  - [x] Verify: `go build ./... && go vet ./... && go test ./...` + `gofmt -l .` + static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build green; the Clients tab renders with no drift badge / export

## Slice 3 — Installer: dashboard always deployed with WG (req 2.2, functional scope)

- [x] `install.sh` always installs the dashboard alongside WireGuard; there is no WG-only path **[Agent: linux-cloud-init]**
  - [x] Remove the WG-only install branch and the "dashboard disabled when `DASHBOARD_RELEASE_TAG` is empty" gating; the dashboard unit is always written/enabled (keep `--dashboard-only` as an update mode)
  - [x] Confirm the admin seed still flows end-to-end (module → `WG_PEERS`/`CLIENTS_JSON` → wg0.conf + SQLite seed) with no seed-specific `install.sh` change
  - [x] Verify: `make shellcheck` green; reason through the always-on path (fresh install + rerun/update both write the dashboard unit)

## Slice 4 — `wg-peer` on-box script (thin client over the local dashboard API) (req 2.5)

- [x] An optional on-box CLI adds/removes/updates a peer via the local dashboard API, equivalent to the UI, with server-side keygen + `--show-config` **[Agent: linux-cloud-init]**
  - [x] New `scripts/wg-peer`: `add <name> [--pubkey KEY] [--show-config]`, `remove <name>`, `update <name> [--name NEW | --pubkey KEY]` — calling `POST` / `DELETE` / `PATCH /api/clients` on the dashboard's local `LISTEN_ADDR`
  - [x] `add` without `--pubkey`: generate an ephemeral keypair (`wg genkey | wg pubkey`), register the public key; with `--show-config`, fetch the dashboard-generated client config (spec-004 endpoint), substitute the generated private key, print to stdout, then discard it (never written to disk/SQLite/S3)
  - [x] `install.sh` installs `/usr/local/bin/wg-peer` and ensures its deps (`wg`, `curl`, `jq` if used)
  - [x] Verify: `make shellcheck`; local test against a stub/localhost API — `add` registers the pubkey, `--show-config` output is a well-formed WG config, `remove`/`update` hit the right endpoints, and the ephemeral private key never lands on disk

## Slice 5 — Owner-run live end-to-end validation (cannot be done in-session)

- [ ] **Owner-run** (after `aws sso login --profile csm`, one dashboard release with the Slice-2/4 binary): fresh AWS deploy → the `admin_peer` connects and the dashboard is reachable; UI add/remove applies live with **no instance replacement**; rebuild the instance → peers restored from the S3 backup; `wg-peer add --show-config` on a manual VPS produces a usable config and the peer appears in the UI. `local` mode — SQLite-only, no S3. **(owner)**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 1 | No live `terraform plan` in-session (expired `csm` SSO) | Agents run `fmt` + `make pre-commit`; owner runs plan/apply |
| Slice 2 | No browser MCP for UI verification | Verify via Go build/tests + template render; visual confirm in owner E2E |
| Slice 4 | No live dashboard API in-session | Test against a stub/localhost API; real API exercised in owner E2E |
| Slice 4 | `wg-peer` is bash; assigned to `linux-cloud-init` | Fits (shell/systemd/bootstrap); `devsecops-quality` is an alternative for the shellcheck gate |
| Slice 5 | Live S3/EC2/VPS not reachable in-session | Owner-run end-to-end |
