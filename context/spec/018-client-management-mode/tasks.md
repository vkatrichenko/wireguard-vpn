# Tasks: Client Management Mode (local | cloud)

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)
- **Status:** In Progress

---

> **Verification reality:** No browser MCP — dashboard UI gating is verified via Go handler/template render tests (`server_test` harness), not a live browser. **No live `terraform plan`/`apply` in-session** — validate is limited to `fmt` + `make pre-commit` (full-config validate needs `aws sso login --profile csm`); the cloud-mode instance replacement is **owner-run**. Everything defaults to `local` → no behavior change on the running box until the owner sets `cloud`.
>
> **Per-slice gate:** dashboard slices → `cd dashboard && go build ./... && go vet ./... && go test ./...` (+ static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build). Terraform slices → `terraform fmt -recursive` + `make pre-commit` (fmt/docs/tflint/trivy); install.sh changes also `make shellcheck`. Each slice leaves the app/config runnable.

---

## Slice 1 — Dashboard: read mode, gate controls + drift badge (cosmetic) (req 2.4)

- [x] Dashboard reads `CLIENT_MANAGEMENT_MODE` and hides all client-mutating controls + the drift badge in cloud mode (cosmetic only), verified by table-driven render tests over `{local, cloud}` **[Agent: go-fullstack]**
  - [x] `cmd/wireguard-dashboard/main.go`: read `getenv("CLIENT_MANAGEMENT_MODE","local")` + validate (warn→fallback `local`), append as the new **last positional arg** to `server.New(...)`
  - [x] `internal/server/server.go`: `New(...)` + `server` struct gain `cloudMode bool`; add `Cloud bool` to `pageData` **and** `clientCountData`; set both in `buildPageData`; gate `computeDrift` (skip → `Drift=0`) when cloud at `server.go:383`
  - [x] `internal/server/handlers_partial_tabs.go`: add `Cloud bool` to `clientsTabData`, set in `buildClientsTabData`, gate `computeDrift` at ~L92
  - [x] `web/templates/tabs/clients.html` + `web/templates/cards/client-count.html`: guard the add form, inline edit toggle+row, remove button, enable/disable toggle, and drift badge with `{{ if not .Cloud }}` / `{{ if and … (not $.Cloud) }}` (list stays read-only visible)
  - [x] Mode-parametrized `newClientsAdminServer` + table-driven render tests: assert all mutating controls + badge absent in cloud, present in local; assert `computeDrift` not invoked in cloud. Full `go test ./...` + build + static arm64 green

## Slice 2 — Terraform (root): consolidate the flag, remove `manage_peers_via_api` (req 2.1, 2.5)

- [x] Root config replaces `manage_peers_via_api` with `client_management_mode` + `enable_restapi_peer_sync`, seed becomes unconditional, restapi re-gated — default `local` is a no-op vs today **[Agent: terraform-aws]**
  - [x] `terraform/dev/locals.tf`: replace `manage_peers_via_api` with `client_management_mode = "local"`; add `enable_restapi_peer_sync = false`; rewrite the spec-017 flag comments
  - [x] `terraform/dev/main.tf`: make the module seed unconditional — `clients_config = local.clients_sorted` (drop the `? [] :` toggle); re-gate `restapi_object.peers` `count` on `local.enable_restapi_peer_sync`; add the "experimental, don't combine with cloud" runbook comment
  - [x] Verify: `terraform fmt -recursive`; `make pre-commit` green; confirm default (`local`, restapi off) has no `restapi_object` and no behavior change vs today

## Slice 3 — Terraform (module): consume the mode — auto-replace + dashboard env (req 2.3, 2.4)

- [x] Module gains a `client_management_mode` input driving cloud-mode instance auto-replace and the dashboard env var; `local` mode replaces nothing **[Agent: terraform-aws]**
  - [x] `terraform/modules/wireguard/variables.tf`: add `client_management_mode` string input (`default "local"`, `validation` `contains(["local","cloud"],…)`); root `main.tf` passes `client_management_mode = local.client_management_mode`
  - [x] `terraform/modules/wireguard/main.tf`: add `terraform_data "peer_replace_trigger"` (`input = var.client_management_mode == "cloud" ? sha256(jsonencode(var.clients_config)) : ""`); add `replace_triggered_by = [terraform_data.peer_replace_trigger]` to the instance lifecycle
  - [x] `terraform/modules/wireguard/locals.tf` + `templates/user-data.txt`: thread `client_management_mode` into the `templatefile()` vars and `export CLIENT_MANAGEMENT_MODE="${client_management_mode}"`
  - [x] `scripts/install.sh`: add `CLIENT_MANAGEMENT_MODE="${CLIENT_MANAGEMENT_MODE:-local}"` default and a static `Environment=CLIENT_MANAGEMENT_MODE=…` line in the dashboard systemd unit
  - [x] Verify: `terraform fmt -recursive`; `make pre-commit` + `make shellcheck` green; confirm `local` mode → replace trigger is the constant `""` (no replacement), config valid

## Slice 4 — Owner-run live end-to-end validation (cannot be done in-session)

- [ ] **Owner-run** (after `aws sso login --profile csm`, one dashboard release cut with the Slice-1 binary): with `client_management_mode = "cloud"`, edit `clients_config` + `terraform apply` → confirm exactly one instance replacement, EIP unchanged, an existing client `.conf` still connects, dashboard client controls + drift badge hidden; set back to `local` → confirm UI management works and a peer edit does not replace the instance **(owner)**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 1 UI verify | No browser MCP | Controls/badge asserted absent/present in Go template render tests, not a live browser |
| Slices 2–3 TF verify | Full-config `terraform validate` needs live AWS creds (expired `csm` SSO) | Agents run `fmt` + `make pre-commit` (+ `shellcheck`); no plan/apply locally |
| Slice 4 | Cloud-mode instance replacement + EIP/reconnect can't run in-session | Owner runs the live E2E; needs a dashboard release carrying the Slice-1 binary |
