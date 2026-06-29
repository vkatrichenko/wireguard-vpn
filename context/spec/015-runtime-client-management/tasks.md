# Tasks: Runtime Client Management

> **Verification reality:** No browser MCP — htmx UI is verified via Go handler tests asserting rendered fragments (the `server_test` harness with dependency fakes), not a real browser. Live `wg syncconf` / tunnel / reboot / instance behavior is **owner-run on the instance** (CLAUDE.md), never claimed from a passing build — collected into **Slice 7**.
>
> **Per-slice gate:** each slice ends green on `cd dashboard && go build ./... && go vet ./... && go test ./...` (plus `make shellcheck` / `terraform fmt`+`validate` for Slice 5). Each slice leaves the app runnable.

---

### Slice 1 — `clients` table + DB CRUD (foundation; app runs unchanged)

- [ ] Add the `clients` table to the `const schema` block in `dashboard/internal/db/db.go` (CREATE TABLE IF NOT EXISTS; `id` INTEGER PK AUTOINCREMENT, `UNIQUE(name)`, `UNIQUE(public_key)`, `UNIQUE(address)`, `enabled`, `note`, `created_at`/`updated_at` unix-seconds). **[Agent: go-fullstack]**
- [ ] Add a `Client` row struct + `ListClients` / `InsertClient` / `UpdateClient` / `DeleteClient` / `CountClients`, mirroring the existing `const q` + `?`-placeholder style. **[Agent: go-fullstack]**
- [ ] Verify: `db_test.go` against `db.Open(ctx, ":memory:")` — CRUD round-trip, uniqueness-constraint violations (dup name/pubkey/address), `CountClients`. `go test ./internal/db/...` green. **[Agent: go-fullstack]**

### Slice 2 — Pure logic: IP allocator, validators, server-conf renderer

- [ ] Promote the WG subnet from the hardcoded `wgconfig` const into a value driven by `WG_SERVER_NET` (env in `main.go`, fallback to the existing `172.16.15.0/24` const); add a pure IP allocator (lowest free `/32`, server `.1` reserved, manual override validated). **[Agent: go-fullstack]**
- [ ] Add pure validators (44-char base64 public key, `x.x.x.x/32` in-subnet, name charset/uniqueness shape) and `wgconfig.BuildServerConf` / `BuildServerPeer` (strings.Builder; enabled clients only; deterministic ordering). **[Agent: go-fullstack]**
- [ ] Verify: table-driven unit tests — allocator (full / fragmented / exhausted subnet, server-IP reserved), validators (valid + each rejection), renderer (disabled omitted, stable order). `go test ./internal/wgconfig/...` green. **[Agent: go-fullstack]**

### Slice 3 — Read path → DB + first-boot seed + drift (UI becomes DB-backed)

- [ ] Add `internal/clients` orchestration service (validation + IP allocation + CRUD behind a write mutex); on startup, seed the `clients` table from `clients.json` when empty. **[Agent: go-fullstack]**
- [ ] Switch `buildClientRows`, `handleGetClients`, `/partial/clients`, and the config-download name lookup (`handlers_config.go`) from `clientsfile` to the DB; retain `clients.json` as the boot seed snapshot only. **[Agent: go-fullstack]**
- [ ] Compute drift (DB clients absent from the boot `clients.json` snapshot) into the Clients-tab view-model + a page-data badge. **[Agent: go-fullstack]**
- [ ] Verify: `server_test` handler tests with a seeded DB fake — clients list + config download resolve from the DB; drift count correct; empty-DB seeds from the clients.json fake. `go test ./internal/server/...` green; `go build ./...`. **[Agent: go-fullstack]**

### Slice 4 — Live-apply service: `wgsync` + staging render (no privileged exec in tests)

- [ ] Add `internal/wgsync`: render the full `wg0.conf` from the DB → write `/var/lib/wireguard-dashboard/wg0.conf.staged` (dashboard-owned) → `Runner`-seam call to `sudo /usr/local/sbin/wg-sync`; wire an idempotent startup reconcile (render + apply after the Slice 3 seed). **[Agent: go-fullstack]**
- [ ] Verify: unit tests with a fake `Runner` — staged file content equals the rendered config, the helper is invoked with the expected argv, reconcile is idempotent (second run = no-op diff). No real `sudo`/filesystem/`wg`. **[Agent: go-fullstack]**

### Slice 5 — Host wiring: `wg-sync` helper, sudoers, subnet env (standalone + EC2)

- [ ] `scripts/install.sh`: install `/usr/local/sbin/wg-sync` (0755 root:root — validate the staging path, `install -m600 -o root -g root` to `/etc/wireguard/wg0.conf`, then `wg syncconf wg0 <(wg-quick strip wg0)`); add the **one** exact-match sudoers line `wireguard-dashboard ALL=(root) NOPASSWD: /usr/local/sbin/wg-sync` inside the existing `visudo -c`-validated block; export `WG_SERVER_NET` into the dashboard systemd unit; ensure `/var/lib/wireguard-dashboard` staging is writable by the dashboard user. **[Agent: linux-cloud-init]**
- [ ] Terraform `modules/wireguard`: confirm `wg_server_net` reaches the dashboard env via the unit (user-data already exports `WG_SERVER_NET`); no `clients_config` semantic change. **[Agent: terraform-aws]**
- [ ] Verify: `make shellcheck` clean; `terraform fmt -recursive` + `terraform validate` + `make pre-commit` (fmt/docs/tflint/trivy) green. (Live apply behavior → Slice 7.) **[Agent: devsecops-quality]**

### Slice 6 — Write endpoints + UI (add / edit / remove / export)

- [ ] Add `dashboard/internal/server/handlers_clients_admin.go`: `POST /api/clients`, `PATCH /api/clients/{name}`, `DELETE /api/clients/{name}`, `GET /api/clients/export?format=hcl|tfvars` — webhook dual-path convention (`isHTMX` → fragment, else JSON; 200 on the htmx validation-failure path); register routes; append new service deps to `New()` (append-only). **[Agent: go-fullstack]**
- [ ] Clients-tab htmx forms (add / edit / enable-disable / remove — fragments keep their own `id` for `outerHTML` swaps, per `webhook.html`), an export button, and the drift badge. **[Agent: go-fullstack]**
- [ ] Verify: `server_test` over htmx (form-encoded) **and** JSON paths — add / edit / delete / export success + validation-failure fragments; assert the `wgsync` fake is invoked on success and **not** on rejection; export emits parseable HCL. `go test ./...` green. **[Agent: go-fullstack]**

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
