# Technical Specification: Terraform-Managed Peers via REST API

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

`clients_config` (root `terraform/dev/main.tf`) becomes the single source of truth that drives **two** consumers derived from one sorted local: the existing first-boot seed (`WG_PEERS` / `CLIENTS_JSON` in user-data) **and** a new `restapi_object` resource that reconciles the running dashboard live. The `Mastercard/restapi` provider (`= 3.0.0`) targets the dashboard over the VPN at `http://172.16.15.1:8080` and manages the whole peer set as **one** object via a new idempotent `PUT /api/clients` endpoint, reading state back from the existing tfvars export for drift detection. On the dashboard side we add a transactional whole-set replace (SQLite → existing `wg syncconf` apply, no tunnel drop) and repoint the spec-015 drift badge to a dashboard-owned "last Terraform-applied" baseline. No auth; VPN reachability is the only gate.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Dashboard — new `PUT /api/clients` (bulk replace)

- **Route:** register `PUT /api/clients → s.handlePutClients` in `internal/server/server.go` (beside the existing `POST /api/clients` at line 263).
- **Request body:** JSON only, the same doc the export emits — `{ "clients_config": [ { "name", "address", "public_key" }, … ] }`. Non-JSON content-type → 400. Every entry **must** carry an explicit `address` (no auto-allocation in the bulk path — required for idempotency); missing/empty address → 400.
- **Response:** 200 with the **canonical body** (identical shape to `GET /api/clients/export?format=tfvars`), so the provider's post-write state equals its subsequent read. Errors: 400 with `{"error": msg}` (reuse `clientErrorStatus`); the htmx path is not used by Terraform but preserved for symmetry.
- **Handler flow:** mirror the `handleAddClient` idiom → parse → `clientsSvc.ReplaceAll(ctx, entries)` → on success re-list and write the canonical JSON.

### 2.2 Dashboard — `clients.Service.ReplaceAll` + transactional DB replace

- **New service method** `ReplaceAll(ctx, []ReplaceEntry) ([]db.Client, error)` in `internal/clients/service.go`, holding `s.mu`, doing: whole-set validation → transactional DB reconcile → **one** `applyLocked(ctx)` (reuses the existing "stage full set → `wg syncconf`" path — no new apply code).
- **Whole-set validation (new):** reuse `ValidatePublicKey` / `ValidateName` / `ValidateAddress` / `ValidateOverride` per field, plus **new intra-payload dedup** (reject duplicate name / key / address *within* the payload) — no existing helper does self-consistency checks. An empty payload is **valid** → reconcile to zero peers. On any failure, **no** DB write and no apply (all-or-nothing).
- **New DB method** `db.ReplaceClients(ctx, []Client) error` wrapping the reconcile in **one transaction** (no clients-table transaction helper exists today — new plumbing, alongside `PruneBefore`): match by `public_key` → update changed rows, insert new, delete absent; **preserve `CreatedAt` / `id` for peers whose `public_key` is unchanged**. Execute **deletes → updates → inserts** to minimize `UNIQUE` collisions on name / key / address.
- **Connected-peer removal:** allowed; log a warning naming each removed peer that currently has a live handshake (from the same `wg show` data the UI uses).

### 2.3 Dashboard — canonical read shape

- Export `exportEntries` (make it public or add a thin wrapper) so the `{name, address, public_key}` projection is shared by the export handler and any diffing. Ordering is already deterministic via `List` (`ORDER BY address ASC, id ASC`). **`read_path` = the existing `GET /api/clients/export?format=tfvars`** — no new read endpoint needed. Runtime-only fields (status, handshake, transfer) are already excluded by the projection, so activity never registers as drift.

### 2.4 Dashboard — repoint the drift badge (spec 015)

- **Problem:** `computeDrift` (`internal/server/server.go:412`) diffs live DB vs `/etc/wireguard-dashboard/clients.json` — a **root-owned, boot-written, dashboard-unwritable** file. It cannot represent "last Terraform-applied set" because the dashboard cannot update it.
- **Change:** introduce a **dashboard-owned baseline in SQLite** (a small `managed_baseline` table, or a single-row KV holding the `{name, address, public_key}` set). `ReplaceAll` writes this baseline in the **same transaction** as the peer replace. Repoint `computeDrift` to diff live enabled peers against this baseline; **fall back to `clients.json`** when the baseline is empty (pre-first-apply), so the badge still works on a freshly-seeded box. Relabel the badge to "diverged from git-managed set" in `web/templates/cards/client-count.html:3` and `web/templates/tabs/clients.html:54`.

### 2.5 Terraform — provider, resource, single-source local

- **`terraform/dev/versions.tf`:** add `restapi = { source = "Mastercard/restapi", version = "= 3.0.0" }` to `required_providers` (exact-pin house format, space after `=`).
- **`terraform/dev/providers.tf`:** add `provider "restapi" { uri = "http://172.16.15.1:8080"; write_returns_object = true }`. No `default_tags` (restapi is not taggable — note for reviewers). Base URI derives from `wg_server_net` (the `.1` host) — a `local`, no data source / EIP output required.
- **Single-source local (`terraform/dev/locals.tf`):** `clients_config` moves to a root `local` (or a new `wireguard` module output) sorted by `address`, consumed by **both** the `module "wireguard"` seed input and the new resource's `data` — guaranteeing they cannot diverge and that the array order matches the dashboard's canonical (address-sorted) read → **no phantom drift**.
- **Resource (`terraform/dev/main.tf`, root module):**

  ```hcl
  resource "restapi_object" "peers" {
    count         = local.manage_peers_via_api ? 1 : 0    # count-gate (see §3)
    path          = "/api/clients"
    object_id     = "managed"                              # static id; singleton
    create_method = "PUT"
    create_path   = "/api/clients"
    read_path     = "/api/clients/export"
    query_string  = "format=tfvars"
    update_method = "PUT"
    update_path   = "/api/clients"
    data          = jsonencode({ clients_config = local.clients_sorted })
    depends_on    = [module.wireguard]
  }
  ```

  Block order per house style (`count` → args → `depends_on`).
- **Destroy semantics (decided):** point destroy at `PUT /api/clients` with `destroy_data = jsonencode({ clients_config = [] })`, so removing the resource empties the managed set consistently. **Caveat:** during a full-stack `terraform destroy` the endpoint is gone and this would error — so pair it with the count-gate (flip the flag off *first*, applying an empty set while the box is still up, or `state rm` the resource). (Alternative considered and rejected: `lifecycle { prevent_destroy = true }`.)

---

## 3. Impact and Risk Analysis

- **System Dependencies:** the resource requires (a) a running instance with the dashboard up **and** (b) the operator connected to the WireGuard tunnel (port 8080 is tunnel-only). **Mitigation — `local.manage_peers_via_api` count-gate (default `false`):** fresh applies bring the box up and seed from `clients_config` as today; the operator flips the flag on once the box is reachable, and subsequent applies reconcile live. Mirrors the existing `count`-gating idiom for opt-in wiring.
- **Boot seed vs. live authority (double-write):** both derive from the *same* `clients_config` local, so on the first flag-on apply the live set already matches `data` → no spurious diff. `clients_config` remains the first-boot seed; `restapi_object` is the ongoing reconciler. `ignore_changes = [user_data]` on the instance is unchanged (peer edits never trigger instance replacement).
- **SQLite `UNIQUE` swap edge:** a single PUT that swaps a unique field between two peers (e.g. A↔B addresses, or rename A→B while adding a new A) can still collide despite delete-before-insert ordering. **Mitigation:** documented limitation — apply such a swap in two steps; the all-or-nothing transaction guarantees no partial state.
- **`CreatedAt` churn:** preserved for unchanged `public_key`s; a re-key legitimately counts as a new peer. Acceptable.
- **Unreachable-endpoint failure:** `plan` / `apply` fails clearly when off-VPN (the provider connects lazily at apply). Intended, documented behavior — not a bug.
- **Auth:** none — VPN-only, consistent with today's already-unauthenticated write endpoints. Out of scope; a future security spec covers the whole write surface.

---

## 4. Testing Strategy

- **Go unit (dashboard):** whole-set validation (intra-payload dedup, empty set valid, missing address rejected, all-or-nothing on failure); `ReplaceClients` transactional reconcile (insert/update/delete mix, `CreatedAt` preservation, delete-before-insert ordering, swap-edge behavior); canonical response equals export shape; `computeDrift` against the new SQLite baseline with `clients.json` fallback. Follow the existing `handlers_clients_admin_test.go` `recordingApplier` pattern to assert exactly one apply per PUT.
- **Idempotency:** PUT the same body twice → the second is a no-op (no DB change, no apply side effects beyond a benign re-sync); a `plan` immediately after `apply` shows no changes (phantom-drift guard).
- **Terraform validate/fmt:** `terraform fmt -recursive`, `terraform validate` in `terraform/dev/`, `make pre-commit`. No `plan` / `apply` against live infra in-session (owner-run; requires a reachable tunnel box).
- **Manual E2E (owner, post-merge, on-VPN):** flag on → declare a peer in git → `apply` reconciles with no tunnel drop; edit a peer in the UI → `plan` shows drift → `apply` reverts; UI-only peer → shows as drift → removed on apply; badge reflects divergence consistently.
