# Tasks: Client Management Mode (local | cloud)

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)
- **Status:** In Progress (re-planned 2026-07-02 around the S3-bridge design; supersedes the interim `ui`/`declared` + instance-replace implementation)

---

> **Design recap:** `client_management_mode = local | cloud` (default `local`). **local** = SQLite-only (spec 015). **cloud** = a versioned S3 object is a UI-authoritative, durable bridge — the dashboard reads it at boot and writes it on every UI edit (no instance replacement); Terraform seeds it once from `clients_config` and **warns** on drift without reverting. The interim `declared`-mode instance-replace trigger, the cosmetic UI-hide, and all `restapi` machinery are removed.
>
> **Canonical JSON contract (shared by TF + Go, anti-phantom-drift):** `[{ "name", "address", "public_key" }]` only (exclude `enabled`/runtime state), sorted by `address` ascending, normalized encoding. Both the Terraform seed/`check` and the dashboard's S3 writes MUST produce this identical shape.
>
> **Verification reality:** No browser MCP — UI/Go behavior via Go tests with a **fake store** (no live AWS). No live `terraform plan`/`apply` in-session (full-config validate needs `aws sso login --profile csm`); S3 seed/drift/reconnect is **owner-run**. Defaults to `local` → no behavior change on a running box until the owner opts into `cloud`.
>
> **Per-slice gate:** dashboard slices → `cd dashboard && go build ./... && go vet ./... && go test ./...` (+ static arm64 build). Terraform slices → `terraform fmt -recursive` + `make pre-commit`; install.sh changes also `make shellcheck`.

---

## Slice 1 — Terraform: finalize mode (local/cloud) + strip the interim machinery (req 2.1, 2.5)

- [x] Root/module Terraform uses `client_management_mode = local | cloud` and the interim declared/restapi machinery is gone; default `local` is a no-op vs today **[Agent: terraform-aws]**
  - [x] Rename values `ui`→`local`, `declared`→`cloud` across `terraform/dev/locals.tf`, `terraform/modules/wireguard/variables.tf` (default + `contains(["local","cloud"])` + description), and all comments
  - [x] Remove `terraform_data.peer_replace_trigger` and the `replace_triggered_by` line from `terraform/modules/wireguard/main.tf`
  - [x] Remove `clients_sorted` from `terraform/dev/locals.tf`; module seed = `local.clients_config`
  - [x] Confirm fully removed: `restapi_object`, `provider "restapi"`, the `Mastercard/restapi` version pin, `enable_restapi_peer_sync`, `dashboard_base_url` (most already done by the owner — verify none remain)
  - [x] Verify: `terraform fmt -recursive` + `make pre-commit` green; default `local` unchanged vs today

## Slice 2 — Dashboard: revert the cosmetic UI-hide, keep mode read (req 2.2, 2.3)

- [x] The dashboard UI is fully functional in both modes again; the `declared`/`cloudMode` hide machinery is removed; mode is still read (drives the store in Slice 4) **[Agent: go-fullstack]**
  - [x] Remove `Declared`/`cloudMode` fields from `pageData`/`clientCountData`/`clientsTabData`, the `computeDrift` gating, and the template guards in `web/templates/tabs/clients.html` + `cards/client-count.html` (restore spec-015 rendering)
  - [x] Delete `internal/server/handlers_clients_declared_mode_test.go`; revert the `newClientsAdminServer(Mode)` helper (keep a store param if Slice 4 needs it)
  - [x] Rename the mode values `ui`→`local`, `cloud` in `main.go`'s `envMode` (default `local`, validate `local`/`cloud`); mode currently just carried, not yet consumed
  - [x] Verify: `go build/vet/test ./...` + static arm64 green; UI renders identically to spec 015

## Slice 3 — Terraform: the S3 bridge (bucket + seed + IAM + drift check + env coords) (req 2.3, 2.4)

- [ ] A versioned S3 bucket + seed object + least-privilege IAM + warn-only drift `check` + dashboard S3 env coords **[Agent: terraform-aws]**
  - [ ] Dedicated `aws_s3_bucket` + `aws_s3_bucket_versioning` (Enabled) + `public_access_block` (all true) + SSE `AES256` + `force_destroy = true` (comment the destroy/versions tradeoff)
  - [ ] `aws_s3_object "clients"` key `clients.json`, `content` = canonical `clients_config` JSON, `content_type = "application/json"`, `lifecycle { ignore_changes = [content, etag] }`
  - [ ] Instance role policy: `s3:GetObject` + `s3:PutObject` on the object ARN only
  - [ ] `check "client_list_drift"` block with scoped `data.aws_s3_object` → assert live body == canonical `clients_config` → warn-only (verify `application/json` yields `body` via terraform MCP; hash-compare fallback)
  - [ ] Thread `CLIENT_MANAGEMENT_MODE` + `CLIENT_STORE_S3_BUCKET` + `CLIENT_STORE_S3_KEY` through module `locals.tf` → `user-data.txt` → `install.sh`; install.sh requires bucket+key when mode is `cloud` (fail-fast, `DASHBOARD_RELEASE_REPO` idiom), static `Environment=` lines
  - [ ] Verify: `terraform fmt -recursive` + `make pre-commit` + `make shellcheck` green

## Slice 4 — Dashboard: S3 client store, boot reconcile + write-through (req 2.3)

- [ ] A client-store abstraction (local no-op + S3-via-`aws`-CLI) wired into boot reconcile and every mutation, with canonical serialization **[Agent: go-fullstack]**
  - [ ] Canonical serializer (fields `{name,address,public_key}`, sort by address, normalized) matching the TF contract; unit-tested
  - [ ] Store interface `Load/Save`; local no-op impl; S3 impl shelling out to `aws s3api get-object`/`put-object`; `Load` distinguishes 404/NoSuchKey from other errors
  - [ ] `main.go` reads `CLIENT_STORE_S3_BUCKET`/`CLIENT_STORE_S3_KEY`, builds the store, passes it to `server.New(...)` (append-only)
  - [ ] Boot reconcile (cloud): load S3 → SQLite → `wg syncconf`; on 404 seed S3 from the local boot seed; non-404 error fails loudly (no clobber)
  - [ ] Write-through: after each client mutation (`ReplaceAll`/`applyLocked` path), Save the canonical list to S3; enable/disable does NOT write to S3
  - [ ] Verify (fake store, no live AWS): boot-load applies, 404-seed, non-404 fail-loud/no-clobber, mutation write-through, enable/disable excluded, canonical-serialization test; full `go test ./...` + build + static arm64 green

## Slice 5 — Owner-run live end-to-end validation (cannot be done in-session)

- [ ] **Owner-run** (after `aws sso login --profile csm`, one dashboard release with the Slice-2/4 binary): `cloud` mode — deploy → S3 object seeded from `clients_config`, operator connects; add a peer in the UI → applies live (no instance replacement), S3 object updates; replace the instance → re-reads S3, keeps the UI-added peer; edit `clients_config` to differ → `terraform plan` shows the drift **warning**, `apply` does **not** revert. `local` mode — spec-015 behavior unchanged, no S3 usage **(owner)**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 3 drift check | `data.aws_s3_object` may not return `body` for all content types | Verify `application/json` yields `body` via terraform MCP; fall back to hash-compare if not |
| Slices 3–4 contract | Canonical JSON must match between TF and Go | Both reference the shared contract note above; add a fixture asserting the two encodings agree |
| Slices 1–3 TF verify | Full-config `terraform validate` needs live AWS creds (expired `csm` SSO) | Agents run `fmt` + `make pre-commit` (+ `shellcheck`); no plan/apply locally |
| Slice 4 Go verify | No live AWS in-session | S3 store behind an interface + faked; live S3 exercised only in owner E2E (Slice 5) |
| Slice 5 | S3 seed/drift/reconnect can't run in-session | Owner runs the live E2E; needs a dashboard release carrying the new binary |
