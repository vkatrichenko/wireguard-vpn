# Technical Specification: Client Management Mode (local | cloud)

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed (2026-07-02 — implemented & owner-validated live as dashboard v0.0.15)
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Introduce a validated `client_management_mode` string (`"local"` | `"cloud"`, default `"local"`) that threads `terraform/dev/locals.tf` → the `wireguard` module → user-data → the dashboard env, replacing the spec-017 `manage_peers_via_api` bool. In **cloud** mode the client list lives in a **versioned S3 object** that the dashboard reads at boot and rewrites on every UI edit, and that Terraform seeds once and watches for drift; in **local** mode there is no external store (spec-015 SQLite only). All of spec-017's `restapi` machinery and the interim `declared`-mode instance-replace trigger are **removed**. There is no instance replacement on peer changes in either mode.

The peer flows:

- **local:** `clients_config` → user-data → `/etc/wireguard-dashboard/clients.json` → dashboard seeds SQLite at boot → UI edits stay in SQLite (`wg syncconf`). Exactly spec 015.
- **cloud:** Terraform seeds `s3://<bucket>/clients.json` once from `clients_config`; the dashboard reads S3 at boot → SQLite → `wg syncconf`; every UI edit writes SQLite + `wg syncconf` + `PutObject` back to S3; Terraform's `plan`/`apply` compares `clients_config` against the live object and **warns** on drift (never reverts).

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Terraform — the mode variable & removing spec 017

- **Root (`terraform/dev/locals.tf`):** `client_management_mode = "local"`. Keep `clients_config`. **Remove** `clients_sorted` (its canonical-ordering role moves into the shared JSON contract, §2.5), `enable_restapi_peer_sync`, and `dashboard_base_url` (only the restapi provider used it).
- **Root (`terraform/dev/main.tf`):** pass `client_management_mode = local.client_management_mode` to the module; module `clients_config` input stays the full `local.clients_config` (unconditional seed — kills the spec-017 cold-start). **Remove** the `restapi_object "peers"` resource.
- **`providers.tf` / `versions.tf`:** **remove** the `provider "restapi"` block and the `Mastercard/restapi` version pin.
- **Module input (`terraform/modules/wireguard/variables.tf`):** `client_management_mode` string var, `default = "local"`, `validation { contains(["local","cloud"], …) }`.

### 2.2 Terraform — the S3 bridge (bucket + seed + IAM)

- **Bucket (module, cloud-relevant but created unconditionally is fine, or `count`-gated on mode == "cloud"):** a dedicated `aws_s3_bucket` (separate from the `force_destroy` health-check bucket). Configure via the split resources: `aws_s3_bucket_versioning` = `Enabled`; `aws_s3_bucket_public_access_block` all-true; `aws_s3_bucket_server_side_encryption_configuration` = `AES256` (SSE-S3, no KMS — list isn't sensitive); `force_destroy = true` (operator's explicit choice — note in a comment that this lets `destroy` wipe the bucket + version history). Four required tags via provider `default_tags`.
- **Seed object:** `aws_s3_object "clients"`, `key = "clients.json"`, `content = <canonical JSON of clients_config>` (§2.5), `content_type = "application/json"`, `lifecycle { ignore_changes = [content, etag] }` so Terraform seeds once and **never** overwrites the UI's writes. **Decision to confirm at build:** `ignore_changes` on `content` means a later `clients_config` edit does *not* re-push to S3 (UI-authoritative) — drift is surfaced by §2.3, not auto-applied.
- **IAM:** extend the instance role with an inline/managed policy granting `s3:GetObject` + `s3:PutObject` on `arn:aws:s3:::<bucket>/clients.json` only (least-privilege; no `ListBucket` needed for a fixed key). Reuse the existing role from the WG-key SSM read.

### 2.3 Terraform — drift detection (warn-only, `check` block)

- A top-level `check "client_list_drift"` block (TF ≥1.5, fine on 1.14.8) with a **scoped `data "aws_s3_object"`** reading the live `clients.json` and an `assert` comparing its `body` to the canonical `clients_config` JSON (§2.5). On mismatch it emits a **warning** (non-blocking) on `plan` and `apply`; it never changes the object.
- **Body availability:** `data.aws_s3_object` returns `body` only for textual content types — confirm via the terraform MCP / provider docs that `application/json` yields `body`; if not, set an explicit text `content_type` on writes or compare a stored hash instead. Comparison must be on **normalized** JSON both sides (§2.5) to avoid phantom drift.
- Lives in the **root** module (`terraform/dev/`), where both `local.clients_config` and the bucket/object are in scope.

### 2.4 Terraform — mode + store coordinates → dashboard env

Thread three values to the dashboard following the existing `DASHBOARD_*` pattern (module `locals.tf` templatefile map → `templates/user-data.txt` exports → `scripts/install.sh` reads → systemd `Environment=`):

1. `CLIENT_MANAGEMENT_MODE` = the mode.
2. `CLIENT_STORE_S3_BUCKET` = the bucket name (empty in local mode).
3. `CLIENT_STORE_S3_KEY` = `clients.json` (empty in local mode).

`scripts/install.sh`: read all three nounset-safe. In the dashboard gate, if `CLIENT_MANAGEMENT_MODE == "cloud"`, **require** a non-empty bucket + key and fail hard otherwise (matching the `DASHBOARD_RELEASE_REPO` idiom); `local` needs neither. Emit the three as static `Environment=` lines in the systemd unit. (This replaces the previous "require the mode when the dashboard is installed" gate with "require the S3 coords when cloud".)

### 2.5 Canonical JSON contract (anti-phantom-drift)

Both Terraform and the dashboard must serialize the list **identically** or the drift `check` will false-positive (the exact lesson from spec 017):

- **Fields:** `[{ "name", "address", "public_key" }]` only. **Exclude** `enabled`/disabled and any SQLite-only runtime state (per functional 2.4) — so a UI enable/disable toggle is *not* git drift.
- **Order:** sort by `address` ascending (the old `clients_sorted` logic, now the canonical rule).
- **Formatting:** agree on one encoding (e.g. compact `jsonencode` output vs Go `json.Marshal`) and normalize before comparison — the `check` should compare parsed/re-encoded canonical forms, not raw bytes, to be robust to whitespace.

### 2.6 Dashboard (Go) — mode + S3 client store

- `cmd/wireguard-dashboard/main.go`: read `CLIENT_MANAGEMENT_MODE` (default `local`, validate/warn→fallback like `envPct`), plus `CLIENT_STORE_S3_BUCKET`/`CLIENT_STORE_S3_KEY`. Construct a **client-store** dependency and pass it to `server.New(...)` (append-only last arg, per convention).
- **Store abstraction:** an interface with `Load(ctx) ([]Entry, error)` / `Save(ctx, []Entry) error`. Two implementations: a **no-op/local** store (cloud disabled → today's behavior) and an **S3** store. The S3 store shells out to the **`aws` CLI** (`aws s3api get-object` / `put-object`) — already on the box, arch-agnostic, avoids pulling in the AWS Go SDK (matches the minimalist IMDSv2-raw-read style). `Load` distinguishes **404/NoSuchKey** (→ signal "empty", let caller seed) from other errors (→ propagate, fail loudly).
- **Boot reconcile (cloud):** extend the existing spec-015 startup reconcile — if cloud: `Load` from S3; on 404 seed S3 from the local `clients.json` boot seed and continue; otherwise make SQLite match S3, then `wg syncconf`.
- **Write-through (cloud):** after any successful client mutation in the clients service (add/edit/remove — the `ReplaceAll`/`applyLocked` path from spec 017 Slice 1 already centralizes this), serialize the canonical list (§2.5) and `Save` to S3. Enable/disable toggles apply to SQLite/`wg` but are **not** written to S3 (excluded field).
- No template/UI changes: the UI is fully functional in both modes. (The spec-018-as-built `Declared`/`cloudMode` view-model fields, `computeDrift` gating, and template guards are **removed/reverted**.)

### 2.7 Revert the spec-018-as-built interim pieces

Remove what the earlier `declared`/trigger implementation added: the module `terraform_data.peer_replace_trigger` + `replace_triggered_by`; the cosmetic template guards + `Declared`/`cloudMode` view-model fields + `computeDrift` gating in Go; the `install.sh` "require mode when dashboard" gate (replaced by §2.4's "require S3 coords when cloud"); and the `restapi`/`enable_restapi_peer_sync` remnants. Keep: the mode variable + threading, the `env`-reading helper, and the go-test call-site plumbing (retargeted to the store).

---

## 3. Impact and Risk Analysis

- **No instance replacement, no tunnel drop** on peer changes in either mode — the core win over both spec 017 (live API) and the interim declared/trigger design.
- **Last-writer-wins to S3.** Two admins editing concurrently → last `PutObject` wins; S3 versioning keeps history for recovery. Acceptable for a small VPN.
- **Boot ordering.** On a cloud rebuild the dashboard must read S3 *before* trusting the (stale) user-data seed. The reconcile treats **S3 as source of truth when present**, local seed only on a definitive 404 — a non-404 S3 error must fail loudly, never silently fall back and clobber.
- **Drift check reads S3 every plan** (a data-source read) and depends on `data.aws_s3_object` returning `body` for JSON — verify in the tech pass; hash-compare fallback if not.
- **`force_destroy = true`** means `terraform destroy` wipes the list + versions. Operator's explicit choice; documented in a resource comment.
- **AWS-only cloud mode.** Standalone VPS has no S3 → cloud mode requires the S3 coords (install.sh fails fast without them); standalone stays local.

---

## 4. Testing Strategy

- **Go:** table-driven tests over the store abstraction — a fake store verifying (a) boot reconcile loads from the store and applies via `wg syncconf`; (b) a 404 seeds the store from the local boot seed; (c) a non-404 error fails loudly and does **not** clobber SQLite; (d) each client mutation writes the canonical list through to the store; (e) enable/disable does **not** write to the store. Canonical-serialization unit test (field subset + address sort + normalized encoding). Full `go build/vet/test` + static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build. No live AWS — the S3 CLI store is behind the interface and faked.
- **Terraform:** `terraform fmt -recursive`; `make pre-commit` (fmt/docs/tflint/trivy) + `make shellcheck` for install.sh. Confirm `local` mode plans no S3 read/write dependency for the box, and the bucket/object/policy shape is valid. **No live plan/apply in-session** (owner-run; full-config validate needs `aws sso login --profile csm`).
- **Owner-run E2E (post-merge):** `cloud` mode — deploy → confirm the S3 object is seeded from `clients_config` and the operator connects; add a peer in the UI → confirm it applies live (no instance replacement) and the S3 object updates; replace the instance → confirm it re-reads S3 and keeps the UI-added peer; edit `clients_config` to differ → `terraform plan` shows the **drift warning** and `apply` does **not** revert the UI list. `local` mode — confirm spec-015 behavior unchanged and no S3 usage.
