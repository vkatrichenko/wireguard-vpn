# Tasks: Terraform-Managed Peers via REST API

> **Verification reality:** No browser MCP — the drift-badge/template change is verified via Go handler/template tests (`server_test` harness), not a live browser. **No live `terraform plan`/`apply` in-session** — the `restapi` provider needs a running, tunnel-reachable box, and plan/apply is owner-run (CLAUDE.md); in-session Terraform is limited to `fmt` + `validate` (+ `terraform init` for the new provider) + `make pre-commit`. Live `wg syncconf` / on-VPN reconcile behavior is **owner-run**, collected into the final slice.
>
> **Per-slice gate:** dashboard slices end green on `cd dashboard && go build ./... && go vet ./... && go test ./...` (+ static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build). Terraform slice ends green on `terraform fmt -recursive`, `terraform validate` in `terraform/dev/`, and `make pre-commit`. Each slice leaves the app/config runnable — the Terraform flag defaults **off**, so nothing changes on a real box until the owner opts in.

---

### Slice 1 — Transactional whole-set replace in the service/DB layer (req 2.1, 2.2)

- [ ] `internal/db/db.go`: add `ReplaceClients(ctx, []Client) error` in a **single transaction** (new tx plumbing, alongside `PruneBefore`) — reconcile by `public_key`: delete absent → update changed → insert new; preserve `CreatedAt`/`id` for unchanged public keys. **[Agent: go-fullstack]**
- [ ] `internal/clients/service.go`: add `ReplaceAll(ctx, []ReplaceEntry) ([]db.Client, error)` holding `s.mu` → whole-set validation → `db.ReplaceClients` → **one** `applyLocked(ctx)` (reuse existing stage→`wg syncconf` path). **[Agent: go-fullstack]**
- [ ] `internal/clients/validate.go` (or a new `validateSet`): whole-set validator reusing per-field validators + **new intra-payload dedup** (duplicate name/key/address within the payload). Empty payload valid (→ zero peers); every entry must carry an explicit `address` (reject empty). **[Agent: go-fullstack]**
- [ ] `internal/clients/export.go`: export `exportEntries` (or thin wrapper) so the `{name,address,public_key}` canonical projection is reusable. **[Agent: go-fullstack]**
- [ ] Verify: `db` tests (insert/update/delete mix in one tx, `CreatedAt` preserved, delete-before-insert ordering, `UNIQUE` swap-edge behavior is all-or-nothing); `clients` tests (intra-payload dedup, empty-set valid, missing-address rejected, exactly one apply via `recordingApplier`). Full `go test ./...` + build green. **[Agent: go-fullstack]**

### Slice 2 — `PUT /api/clients` endpoint + canonical response (req 2.3)

- [ ] `internal/server/server.go`: register `PUT /api/clients → s.handlePutClients` beside the existing `POST` (line ~263). **[Agent: go-fullstack]**
- [ ] `internal/server/handlers_clients_admin.go`: add `handlePutClients` — JSON-only body `{clients_config:[{name,address,public_key}]}` (non-JSON → 400), call `clientsSvc.ReplaceAll`, respond **200 with the canonical body** (same shape as `GET /api/clients/export?format=tfvars`); errors via `clientErrorStatus` → `{"error":msg}`. **[Agent: go-fullstack]**
- [ ] Verify: server handler tests — full-set PUT applies; empty set → zero peers; duplicate/invalid → 400 with no apply; missing address → 400; **idempotent re-PUT is a no-op** (asserted via `recordingApplier`); response body byte-equals the export for the same set. `curl -X PUT` smoke against the test server. Full `go test ./...` + build green. **[Agent: go-fullstack]**

### Slice 3 — Repoint the drift badge to a dashboard-owned baseline (req 2.5)

- [ ] `internal/db/db.go`: add a dashboard-owned baseline store (small `managed_baseline` table or single-row KV of the `{name,address,public_key}` set) + read/write helpers. **[Agent: go-fullstack]**
- [ ] `internal/clients/service.go`: `ReplaceAll` writes the baseline **in the same transaction** as the peer replace. **[Agent: go-fullstack]**
- [ ] `internal/server/server.go`: repoint `computeDrift` to diff live enabled peers vs the SQLite baseline, **falling back to `clients.json`** when the baseline is empty (pre-first-apply). **[Agent: go-fullstack]**
- [ ] `web/templates/cards/client-count.html` + `web/templates/tabs/clients.html`: relabel the badge to "diverged from git-managed set." **[Agent: go-fullstack]**
- [ ] Verify: Go tests — baseline written on PUT; drift computed against baseline; `clients.json` fallback when baseline empty; template renders the new label. Full `go test ./...` + build green. **[Agent: go-fullstack]**

### Slice 4 — Terraform provider + resource wiring (validate-only in-session) (req 2.1, 2.6)

- [ ] `terraform/dev/versions.tf`: add `restapi = { source = "Mastercard/restapi", version = "= 3.0.0" }` to `required_providers`. **[Agent: terraform-aws]**
- [ ] `terraform/dev/providers.tf`: add `provider "restapi" { uri = "http://172.16.15.1:8080"; write_returns_object = true }` (no `default_tags` — not taggable; note for reviewers). **[Agent: terraform-aws]**
- [ ] `terraform/dev/locals.tf`: introduce `local.manage_peers_via_api` (bool, default `false`), move `clients_config` into a root local sorted by `address` (`local.clients_sorted`) feeding **both** the `module "wireguard"` seed input and the resource `data`; derive the base URI host from `wg_server_net`. **[Agent: terraform-aws]**
- [ ] `terraform/dev/main.tf`: add `restapi_object "peers"` — `count = local.manage_peers_via_api ? 1 : 0`, PUT create/update on `/api/clients`, `read_path=/api/clients/export` + `query_string=format=tfvars`, static `object_id="managed"`, `data = jsonencode({clients_config = local.clients_sorted})`, `destroy_data = jsonencode({clients_config = []})`, `depends_on = [module.wireguard]`. Block order `count → args → depends_on`. **[Agent: terraform-aws]**
- [ ] Verify: `terraform fmt -recursive`; `terraform init` (fetch the new provider) + `terraform validate` in `terraform/dev/` — with the flag **off**, the plan graph contains no `restapi_object` and existing behavior is unchanged; `make pre-commit` (fmt/docs/tflint/trivy) green. **No live plan/apply** (owner-run, Slice 5). **[Agent: terraform-aws]**

### Slice 5 — Owner-run live end-to-end validation (cannot be done in-session)

- [ ] **Owner-run**, connected to the VPN, against a running box: flip `manage_peers_via_api = true`; `terraform apply` reconciles the declared set with **no tunnel drop** and no instance replacement; declare a new peer in git → apply adds it live; edit a peer in the UI → `plan` shows drift → `apply` reverts it; create a UI-only peer → shows as drift → removed on apply; empty `clients_config` → apply removes all (plan shows it first); drift badge reflects divergence consistently; confirm off-VPN `plan` fails with a clear connection error. **(owner)**

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 3 badge verify | No browser MCP | htmx/template label asserted in Go template tests, not a live browser |
| Slice 4 verify | New `Mastercard/restapi` provider needs `terraform init` (registry network); `terraform validate` may hit the known AWS-provider plugin-start flake (warm the binary, don't thrash) | Agent runs `fmt`/`init`/`validate`/`make pre-commit` only; **no** plan/apply locally |
| Slice 5 | Live on-VPN `apply` + `wg syncconf` reconcile can't run in-session (needs a reachable tunnel box; owner runs Terraform per CLAUDE.md) | Owner runs the full live E2E in Slice 5 |
