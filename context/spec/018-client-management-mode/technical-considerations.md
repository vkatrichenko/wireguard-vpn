# Technical Specification: Client Management Mode (local | cloud)

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Introduce a validated `client_management_mode` string local (`"local"` | `"cloud"`, default `"local"`) that threads from `terraform/dev/locals.tf` → the `wireguard` module → user-data → the dashboard env, replacing the spec-017 `manage_peers_via_api` bool. In `cloud` mode a module-level `terraform_data` keyed on a hash of `clients_config` drives `replace_triggered_by` on the instance, so a peer change auto-replaces the box; in `local` mode that hash is a constant so nothing replaces. The dashboard reads `CLIENT_MANAGEMENT_MODE`, carries a `cloudMode` flag into its three view-models, and hides all client-mutating controls + the drift badge (cosmetically) in cloud mode. Spec-017's `restapi_object`/provider stay in the tree behind a new off-by-default `enable_restapi_peer_sync` flag. The peer seed becomes unconditionally the full `clients_config`.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Terraform — the mode variable & subsuming spec 017

- **Root (`terraform/dev/locals.tf`):** replace `manage_peers_via_api` with `client_management_mode = "local"`; add `enable_restapi_peer_sync = false`. Keep `clients_config` / `clients_sorted` / `dashboard_base_url`. Rewrite the spec-017 flag comments.
- **Module input (`terraform/modules/wireguard/variables.tf`):** new `client_management_mode` string var, `default = "local"`, `validation { contains(["local","cloud"], …) }`. String enum (not a derived bool) — self-documenting, threads to the dashboard env unchanged, room for a future mode.
- **Seed is now unconditional (`terraform/dev/main.tf`, the module `clients_config` input):** `clients_config = local.clients_sorted` (drop the spec-017 `local.manage_peers_via_api ? [] : …` toggle). Both modes seed the full set at boot — this is what kills the spec-017 cold-start lockout.

### 2.2 Terraform — cloud-mode auto-replace

- **New module resource** `terraform_data "peer_replace_trigger"` (in `terraform/modules/wireguard/`) with `input = var.client_management_mode == "cloud" ? sha256(jsonencode(var.clients_config)) : ""`.
- **Instance lifecycle** (`terraform/modules/wireguard/main.tf:53-56`) gains `replace_triggered_by = [terraform_data.peer_replace_trigger]`, kept alongside `create_before_destroy` + `ignore_changes = [user_data, user_data_base64]`. `replace_triggered_by` (TF ≥1.2) overrides `ignore_changes` and composes cleanly; it must live in the module (same-module reference rule). `terraform_data` requires TF ≥1.4 — both fine on the pinned 1.14.8. Local mode → constant `""` → never fires. Cloud mode → peer-set hash → any peer change replaces.
- **Persistence across replace (asserted):** the EIP (`aws_eip` + `aws_eip_association`, `main.tf:1-8,84-99`) is a separate resource that re-associates to the new instance; the server key comes from SSM at boot. Public endpoint + server pubkey are stable → **existing client `.conf` files stay valid**; the new box boots already seeded with the declared peers. (Note: `eip_id`/`use_eip` are dead pass-throughs in user-data — re-association is 100% the Terraform `aws_eip_association` resource; the spec must not imply user-data re-attaches the EIP.)

### 2.3 Terraform — mode → dashboard env

Thread `CLIENT_MANAGEMENT_MODE` following the existing `DASHBOARD_*` pattern:

1. `terraform/modules/wireguard/locals.tf` — add `client_management_mode = var.client_management_mode` to the `templatefile()` var map.
2. `terraform/modules/wireguard/templates/user-data.txt` — add `export CLIENT_MANAGEMENT_MODE="${client_management_mode}"` beside the other `DASHBOARD_*` exports.
3. `scripts/install.sh` — add a default (`CLIENT_MANAGEMENT_MODE="${CLIENT_MANAGEMENT_MODE:-local}"`) near the other reads, and a **static `Environment=CLIENT_MANAGEMENT_MODE=…` line** in the dashboard systemd unit heredoc. Not secret/optional → a plain `Environment=` line, not `alerts.env`.

### 2.4 Terraform — restapi demotion

Re-gate `restapi_object.peers` `count` on the new `local.enable_restapi_peer_sync` (default `false`) instead of `manage_peers_via_api`. The `provider "restapi"` block + `versions.tf` require stay (an unused provider with a count-0 resource is inert — lazy, only costs a provider download at init). A runbook comment marks it **experimental — not for normal use, don't combine with `cloud` mode** (peer double-ownership). Cross-variable `validation` (TF ≥1.9) is optional hardening, not required. `clients_sorted` / `dashboard_base_url` stay (still consumed by the resource body + provider `uri`).

### 2.5 Dashboard (Go) — read & thread the mode

- `dashboard/cmd/wireguard-dashboard/main.go`: `getenv("CLIENT_MANAGEMENT_MODE","local")` + validate (warn→fallback `local`, matching the `envPct`/`envDuration` convention); append as the new **last positional arg** to `server.New(...)` (the file's documented append-only convention).
- `internal/server/server.go`: `New(...)` signature + the `server` struct gain `cloudMode bool` (derived `mode == "cloud"`); set in the constructor literal.
- **Three view-models** each gain `Cloud bool` (the codebase keeps per-fragment structs, no single choke point): `pageData` (`server.go:133-159`), `clientCountData` (`server.go:122-131` — note `client-count.html` receives `.ClientCount` as its dot), and `clientsTabData` (`handlers_partial_tabs.go:59-75`). Set `data.Cloud`/`data.ClientCount.Cloud` in `buildPageData`, and `data.Cloud` in `buildClientsTabData`.
- **Gate `computeDrift`** at both call sites (`server.go:383`, `handlers_partial_tabs.go:92`): skip the call (leave `Drift = 0`) when `cloudMode` — no wasted work, badge hidden. `computeDrift`'s own signature/semantics are unchanged.

### 2.6 Dashboard — template guards (cosmetic)

In cloud mode, hide via `{{ if not .Cloud }}` (top-level blocks) / `{{ if and … (not $.Cloud) }}` (inside `{{ range .Rows }}`, using `$` for the top-level dot):

- `dashboard/web/templates/tabs/clients.html`: the **add form** (~L63-85), the **inline edit toggle** (~L136) + **edit row** (~L151), the **remove button** (~L142), **and the enable/disable toggle** (~L137-141 — it also mutates via `/api/clients` PATCH), plus the **drift badge** (~L54).
- `dashboard/web/templates/cards/client-count.html`: the **drift badge** (L3, dot is `clientCountData` → `{{ if and .Drift (not .Cloud) }}`).

The client list stays visible (read-only). **Enforcement is cosmetic only** — endpoints are unchanged; a hand-crafted write still mutates SQLite but is pointless (the next `apply` re-provisions from `clients_config`). No handler `403`s (deliberate, per the functional spec).

---

## 3. Impact and Risk Analysis

- **Cloud peer edit = full instance replace.** `create_before_destroy` means new box up → EIP re-associates → old box destroyed, with a brief dual-instance window and a short tunnel drop for all peers. Expected and documented; acceptable for infrequent peer changes.
- **Local-mode `clients_config` edits are inert on a running box** (trigger constant, `ignore_changes` on user_data) — they take effect only on the next rebuild, re-seeding and overwriting UI-added peers (existing spec-015 tradeoff). Documented.
- **Cosmetic hiding ≠ enforcement.** A direct API call in cloud mode still mutates SQLite until the next apply reverts it. Accepted; Terraform re-provisioning is the reconciler.
- **Mode × restapi flag** can express a nonsensical combo (both own peers) — mitigated by a runbook comment; optional cross-variable validation.
- **Append-only `server.New`** ripples to every test call site (`newClientsAdminServer`) — handled via a mode-parametrized test constructor so existing calls default to `local`.

---

## 4. Testing Strategy

- **Go:** table-driven render tests over `mode ∈ {local, cloud}` using a mode-parametrized `newClientsAdminServer`; assert the add form / edit toggle / remove / enable-disable / drift badge are **absent** in cloud and **present** in local (substring predicates like the existing `driftBadgePresent` / `getClientsPartial` in `handlers_clients_drift_test.go`); assert `computeDrift` isn't invoked in cloud (drift 0). Full `go build/vet/test` + static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build.
- **Terraform:** `terraform fmt -recursive`; `make pre-commit` (fmt/docs/tflint/trivy). Validate that with `client_management_mode = "local"` the plan graph has no `restapi_object` and the replace trigger is a constant. **No live plan/apply in-session** (owner-run; full-config validate needs `aws sso login --profile csm`).
- **Owner-run E2E (post-merge):** flip to `cloud`, edit `clients_config`, `apply` → confirm exactly one instance replacement, EIP unchanged, an existing client `.conf` still connects, UI controls hidden; flip to `local` → confirm UI management works and no replace on peer edits.
