# Tasks: Runtime Client Management

> **Verification reality:** No browser MCP — htmx UI is verified via Go handler tests asserting rendered fragments (the `server_test` harness with dependency fakes), not a real browser. Live `wg syncconf` / tunnel / reboot / instance behavior is **owner-run on the instance** (CLAUDE.md), never claimed from a passing build — collected into **Slice 7**.
>
> **Per-slice gate:** each slice ends green on `cd dashboard && go build ./... && go vet ./... && go test ./...` (plus `make shellcheck` / `terraform fmt`+`validate` for Slice 5). Each slice leaves the app runnable.

---

### Slice 1 — `clients` table + DB CRUD (foundation; app runs unchanged)

- [x] Add the `clients` table to the `const schema` block in `dashboard/internal/db/db.go` (CREATE TABLE IF NOT EXISTS; `id` INTEGER PK AUTOINCREMENT, `UNIQUE(name)`, `UNIQUE(public_key)`, `UNIQUE(address)`, `enabled`, `note`, `created_at`/`updated_at` unix-seconds). **[Agent: go-fullstack]**
- [x] Add a `Client` row struct + `ListClients` / `InsertClient` / `UpdateClient` / `DeleteClient` / `CountClients`, mirroring the existing `const q` + `?`-placeholder style. **[Agent: go-fullstack]**
- [x] Verify: `db_test.go` against `db.Open(ctx, ":memory:")` — CRUD round-trip, uniqueness-constraint violations (dup name/pubkey/address), `CountClients`. `go test ./internal/db/...` green. **[Agent: go-fullstack]** _(verified 2026-06-30; UpdateClient keyed by id, Note as sql.NullString, caller-stamped timestamps.)_

### Slice 2 — Pure logic: IP allocator, validators, server-conf renderer

- [x] Promote the WG subnet into a value driven by `WG_SERVER_NET` (pure `ParseServerNet`, fallback to the new exported `wgconfig.DefaultServerNet` const); add a pure IP allocator `AllocateAddress` (lowest free `/32`, server IP + network/broadcast reserved, fragmented-gap fill, `ErrSubnetExhausted`, manual override validated). **[Agent: go-fullstack]** _(in new `internal/clients` package; `os.Getenv` wiring deferred to Slice 3 to avoid dead code.)_
- [x] Add pure validators (`ValidatePublicKey` base64-44, `ValidateAddress` `/32` in-subnet, `ValidateName` charset, `ValidateOverride`) and `wgconfig.BuildServerConf` / `BuildServerPeer` (strings.Builder; enabled clients only; ascending-IP order; host-specific PostUp/PostDown passed in to stay pure). **[Agent: go-fullstack]**
- [x] Verify: table-driven unit tests — allocator (empty / fragmented / exhausted, server-IP reserved, valid+invalid override), validators (valid + each rejection), renderer (disabled omitted, stable order, stanza fields). `go test ./internal/clients/... ./internal/wgconfig/...` green. **[Agent: go-fullstack]** _(verified 2026-06-30.)_

### Slice 3 — Read path → DB + first-boot seed + drift (UI becomes DB-backed)

- [x] Add `internal/clients` orchestration service (validation + IP allocation + CRUD behind a write mutex); on startup, seed the `clients` table from `clients.json` when empty. **[Agent: go-fullstack]** _(`service.go`: `Service` with DB + `ServerNet` + mutex + `Applier` seam; `Seed/List/Add/Update/Delete`.)_
- [x] Switch `buildClientRows`, `handleGetClients`, `/partial/clients`, and the config-download name lookup (`handlers_config.go`) from `clientsfile` to the DB; retain `clients.json` as the boot seed snapshot only. **[Agent: go-fullstack]** _(also `handlers_snapshot.go`; `clientsfileSvc` kept for seed + drift baseline.)_
- [x] Compute drift (DB clients absent from the boot `clients.json` snapshot) into the Clients-tab view-model + a page-data badge. **[Agent: go-fullstack]** _(`computeDrift`; `.drift-badge` in clients.html / client-count.html / app.css.)_
- [x] Verify: handler tests with seeded in-memory DB — clients list + config download resolve from the DB; drift count correct; empty-DB seeds. Full `go test ./...` green; `go build ./...`. **[Agent: go-fullstack]** _(verified 2026-06-30.)_

### Slice 4 — Live-apply service: `wgsync` + staging render (no privileged exec in tests)

- [x] Add `internal/wgsync`: render **peers-only** staging file (dashboard can't read the 0600 server key) → `/var/lib/wireguard-dashboard/peers.conf` (0640) → `Runner`-seam call to `sudo /usr/local/sbin/wg-sync`; injected via `clientsSvc.SetApplier`, idempotent non-fatal startup `Reconcile` after seed. **[Agent: go-fullstack]** _(added `wgconfig.BuildServerPeers`; helper merges on-disk `[Interface]` in Slice 5.)_
- [x] Verify: unit tests with a fake `Runner` — staged content = rendered peers (enabled-sorted, disabled omitted, no PrivateKey), mode 0640, exact `sudo <helper>` argv, `*exec.ExitError` stderr-wrapped, idempotent bytes; `t.TempDir()`. `go test ./...` green. **[Agent: go-fullstack]** _(verified 2026-06-30.)_

> **Slice 5 contract (load-bearing):** staged path `/var/lib/wireguard-dashboard/peers.conf`; helper argv `sudo /usr/local/sbin/wg-sync` (must match the sudoers NOPASSWD entry char-for-char); helper merges the on-disk `[Interface]` block + staged peers, then `wg syncconf`.

### Slice 5 — Host wiring: `wg-sync` helper, sudoers, subnet env (standalone + EC2)

- [x] `scripts/install.sh`: install `/usr/local/sbin/wg-sync` (0755 root:root — **merges** the on-disk `[Interface]` block + staged `peers.conf`, writes wg0.conf 0600, then `wg syncconf wg0 <(wg-quick strip wg0)`; idempotent, key never leaves root); add the **one** exact-match sudoers line `wireguard-dashboard ALL=(root) NOPASSWD: /usr/local/sbin/wg-sync` inside the existing `visudo -c` block; add `Environment=WG_SERVER_NET=...` to the dashboard unit. All inside the `DASHBOARD_RELEASE_TAG` gate. **[Agent: linux-cloud-init]**
- [x] Terraform: confirmed `user-data.txt` already exports `WG_SERVER_NET` (no change); **bumped `install_script_sha256`** in `dev/main.tf` (`7be62a7…`→`369447a8…`) since install.sh changed — required for the spec-014 fetch-at-boot checksum. `clients_config` untouched. **[Agent: terraform-aws]**
- [x] Verify: `make shellcheck` clean; `terraform fmt` + `validate` (dev/ Success) + `make pre-commit` (fmt/docs/tflint/trivy) green; sha pin == `shasum scripts/install.sh` confirmed. (Live apply behavior → Slice 7.) **[Agent: devsecops-quality / terraform-aws]** _(verified 2026-06-30.)_

> **Owner-gated deploy ordering:** `install_script_ref` defaults to `main` (content-pinned by sha). The new `install.sh` MUST be pushed to `main` **before** any EC2 apply, else the boot fetch returns the old script, the sha won't match `369447a8…`, and boot aborts. Sequence: push install.sh to main → `plan` → review → `apply`.

### Slice 6 — Write endpoints + UI (add / edit / remove / export)

- [x] Add `handlers_clients_admin.go`: `POST /api/clients`, `PATCH /api/clients/{name}`, `DELETE /api/clients/{name}`, `GET /api/clients/export?format=hcl|tfvars` — webhook dual-path (`isHTMX` → `clients-card` fragment, else JSON; 200 on htmx validation-failure; 404 via new `clients.ErrNotFound` sentinel; 503 nil-service). **[Agent: go-fullstack]** _(export renderers `ExportHCL`/`ExportTFVars` in `internal/clients/export.go`.)_
- [x] Clients-tab htmx forms (add / edit / enable-disable / remove — reusable `{{define "clients-card"}}` fragment keeps its own `id`; geo-map moved OUTSIDE the swap target so its JS markers survive), export links, drift badge; per-row controls `stopPropagation` to not trigger row-expand. **[Agent: go-fullstack]** _(`buildClientsTabData` shared by partial route + write handlers; `ClientRow` gained `Note`/`Enabled`.)_
- [x] Verify: handler tests over htmx (form) **and** JSON — add/edit/delete/export success + validation-failure + 404 + 503; recording fake `Applier` asserts live-apply fires on success and **never** on rejection; HCL parses, tfvars valid JSON. Full `go test ./...` + static `linux/amd64` build green. **[Agent: go-fullstack]** _(verified 2026-06-30.)_

### Slice 7 — Owner-run end-to-end validation (cannot be done in-session)

- [ ] **Owner-run:** deploy the new dashboard build to the instance and confirm, over the tunnel: add a client in the UI → it connects with **no drop** to other peers; remove → cut off immediately; edit/disable → applied live; export → valid `clients_config` HCL; drift badge reflects an un-exported client; **reboot → clients persist**; `cloud-init`/service logs clean. **(owner)**

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slices 1–6 UI verify | No browser MCP | htmx fragments asserted in Go handler tests (`server_test`), not a live browser |
| Slice 5 / 7 | `wg syncconf`, `sudo`, live tunnel, reboot can't run in-session (macOS dev host; instance access is owner-only) | Agents do shellcheck / fmt / validate / unit tests; owner runs the live E2E in Slice 7 |
| Slice 5 | New privileged `wg-sync` + sudoers line touches the "read-only by design" posture | Owner-approval gate — one exact-match (no-wildcard) entry, auditable helper (per approved tech spec) |
| Slice 5 / 7 | EC2 rebuild re-seeds from Terraform → runtime-only clients lost unless exported | Accepted tradeoff (functional spec §3); export + drift badge are the mitigation |
