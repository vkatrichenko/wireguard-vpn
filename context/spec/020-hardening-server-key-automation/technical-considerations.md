<!--
Technical spec for spec 020 — Hardening & Server-Key Automation.
Structures & contracts, not implementations.
-->

# Technical Specification: Hardening & Server-Key Automation

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Three independent workstreams, each verifiable on its own:

- **A — Dashboard (Go).** Two surgical fixes: repoint the client *detail* and *history* handlers from the static boot manifest to the runtime DB (matching the already-migrated config handler), and give the cloud-mode S3 backup a self-heal recheck + an operator-visible health signal.
- **B — Server-key automation (Terraform + AWS user-data wrapper).** Stop threading the WireGuard server private key through Terraform. The instance resolves its key from SSM at boot (reuse-or-generate), so the key never enters state or the launch template. Terraform owns the SSM parameter *shell* (sentinel + `ignore_changes`) for clean destroy and plan-time IAM scoping. The public key is published to a non-secret SSM String param and the installer stdout. The instance IAM role is decoupled from the `use_eip` toggle (a prerequisite, and a latent-bug fix).
- **C — Infra hardening (Terraform + one Go tidy).** A batch of posture fixes to the launch template, buckets, security groups, and dead config.

Affected areas: `dashboard/internal/{server,clients,poller,clientstore}`, `dashboard/web/templates`, `terraform/modules/wireguard/*`, `terraform/modules/network/vpc/main.tf`, `terraform/dev/{main.tf}`, `terraform/dev/backend/main.tf`, `scripts/install.sh` (minimal), and the AWS user-data wrapper `terraform/modules/wireguard/templates/user-data.txt`.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Workstream A — Dashboard fixes

**A1 — Detail & history resolve against the runtime DB.**
- [handlers_clients.go:151](../../../dashboard/internal/server/handlers_clients.go#L151) (`handleGetPartialClientDetail`) and [:302](../../../dashboard/internal/server/handlers_clients.go#L302) (`handleGetClientHistory`) currently call `s.clientsfileSvc.Load()` (the static Terraform boot manifest). Repoint both to resolve name→client against `s.clientsSvc.List(ctx)` — the exact pattern `handleGetClientConfig` already uses ([handlers_config.go:41-59](../../../dashboard/internal/server/handlers_config.go#L41-L59)).
- No route/signature changes; the row template already builds the detail URL from the DB-sourced `ClientRow.PublicKey`.

**A2 — Backup self-heal recheck + health signal.**
- **Recheck method:** add `Service.RecheckStore(ctx) error` to `internal/clients/service.go`. It performs a Load-only probe reusing the existing self-heal branch logic in `saveStoreLocked` ([service.go:485-508](../../../dashboard/internal/clients/service.go#L485)): under lock, when `storeReady == false`, attempt `store.Load`; on success **or** `ErrNotFound`, `setStoreReady(true)` and log "reachable again". A no-op when already ready or store is `NoopStore`.
- **Periodic trigger:** hook the recheck onto the poller's existing hourly retention tick (`DefaultRetentionEvery = 1h`, [poller.go:73](../../../dashboard/internal/poller/poller.go#L73)) rather than adding a new ticker. Introduce a small interface (e.g. `StoreRechecker { RecheckStore(context.Context) error }`), pass the clients service into `poller.New` (new trailing param), and call it in the retention goroutine after `PruneBefore`. In local mode pass a nil/Noop rechecker so the tick skips it.
- **Health endpoint:** convert the free function `handleHealth` ([server.go:295](../../../dashboard/internal/server/server.go#L295)) into a method `s.handleHealth`; add `"client_store_ready": bool` to the JSON **only when** `s.clientManagementMode == "cloud"` (omit entirely in local mode). This makes the already-stored-but-unused `clientsSvc`/`clientManagementMode` fields load-bearing.
- **UI badge:** add a client-store health row to the About view (`dashboard/web/templates/tabs/about.html` / the `cards/about-*.html` set) — "Client store: OK / OFFLINE" shown only in cloud mode; fed by `s.clientsSvc.StoreReady()` via the about handler.
- **Optional (note, not required):** expose the same bool as a `/metrics` gauge via the existing `MetricsProvider` so it can drive the existing alert transports. Deferred unless trivial.

### 2.2 Workstream B — Server-key automation

**Boot flow (AWS user-data wrapper `templates/user-data.txt`):**
1. **Reorder:** install the AWS CLI *before* the key step (today it installs at the tail, [user-data.txt:94-109](../../../terraform/modules/wireguard/templates/user-data.txt#L94)). The SSM calls need it.
2. **Resolve (pre-install):** `aws ssm get-parameter --with-decryption` on the private-key param name (threaded in as a templatefile var). If the value is a valid 44-char WG key → `export WG_SERVER_PRIVATE_KEY=<value>`. If missing / `ParameterNotFound` / equals the sentinel `UNINITIALIZED` → leave the env var empty.
3. **Install:** run `install.sh` unchanged — its existing precedence (`env → /etc/wireguard/server.key → wg genkey`, [install.sh:327-341](../../../scripts/install.sh#L327)) reuses the SSM key when supplied, or generates + persists one when not. `install.sh` stays cloud-agnostic (no AWS calls).
4. **Publish (post-install):** read the effective private key from `/etc/wireguard/server.key`; if step 2 found no real key in SSM, `aws ssm put-parameter --overwrite` it (SecureString). Always derive the public key (`wg pubkey < /etc/wireguard/server.key`) and `put-parameter --overwrite` the String public param. A failed private-key `put-parameter` must be **fatal before** the S3 `.ready` signal, so a missing IAM grant surfaces loudly instead of silently leaving an unpersisted key.

**Terraform — new resources** (module, e.g. a new `server_key.tf`):

| Resource | Type | Key attributes |
|---|---|---|
| `aws_ssm_parameter.wg_server_private_key` | SecureString | name from `${project}-${env}`; `value = "UNINITIALIZED"`; `lifecycle { ignore_changes = [value] }` |
| `aws_ssm_parameter.wg_server_public_key` | String | same name stem `.../server-public-key`; `value = "UNINITIALIZED"`; `ignore_changes = [value]` |

- State only ever holds the sentinel; the instance overwrites the real values. Clean `destroy` (no orphan); ARNs known at plan time for IAM scoping.
- Thread both param **names** into `user-data.txt` via the existing `templatefile()` call ([locals.tf:63](../../../terraform/modules/wireguard/locals.tf#L63)).

**Terraform — removals (the TF read path):**
- `data.aws_ssm_parameter.wg_server_private_key` ([datasource.tf:1-3](../../../terraform/modules/wireguard/datasource.tf#L1)).
- `local.wg_server_private_key` ([locals.tf:71](../../../terraform/modules/wireguard/locals.tf#L71)) and the `wg_server_private_key` templatefile arg + the `WG_SERVER_PRIVATE_KEY="${wg_server_private_key}"` export in `user-data.txt`.
- `var.wg_server_private_key_param` ([variables.tf:87](../../../terraform/modules/wireguard/variables.tf#L87)) and the root assignment at [dev/main.tf:21](../../../terraform/dev/main.tf#L21).
- (The alert-secret data sources stay — those remain operator out-of-band.)

**B4 — Decouple the IAM role from `use_eip`** ([iam.tf](../../../terraform/modules/wireguard/iam.tf)):
- Remove `count = var.use_eip ? 1 : 0` from `aws_iam_policy.wireguard_policy`, `aws_iam_role.wireguard_role`, `aws_iam_role_policy_attachment.wireguard_roleattach`, `aws_iam_role_policy_attachment.wireguard_ssm_core`, and `aws_iam_instance_profile.wireguard_profile` — make them unconditional. Update the `[0]` index references (attachments, profile, and the instance-profile reference in the launch template).
- Move the `ec2:AssociateAddress` statement (currently always built, [iam.tf:15-21](../../../terraform/modules/wireguard/iam.tf#L15)) into a `dynamic "statement" { for_each = var.use_eip ? [1] : [] }` — the only EIP-conditional grant.
- **Add `moved` blocks** for each de-counted resource (`from = ...[0]` → `to = ...`) so Terraform treats them as address moves, **not** destroy/recreate (recreating the role/profile would detach it from the running instance).
- **Add IAM statements** to `wireguard_policy_doc`, scoped to the new param ARNs:
  - `ssm:GetParameter` + `ssm:PutParameter` on the private-key param ARN.
  - `ssm:PutParameter` on the public-key param ARN.
  - For the SecureString: default `alias/aws/ssm` needs no extra KMS grant beyond the SSM actions when the role is in the same account; a dedicated CMK (optional hardening) would add `kms:Decrypt`/`kms:Encrypt` with a `kms:ViaService = ssm.us-east-1.amazonaws.com` condition. **Assumption: use the AWS-managed `alias/aws/ssm` key** (simplest; the key is instance-generated and never in state) — flag if a CMK is wanted.

### 2.3 Workstream C — Infra hardening

| Item | Location | Change |
|---|---|---|
| **C1** State-bucket KMS | [dev/backend/main.tf](../../../terraform/dev/backend/main.tf) | Switch SSE-S3 → SSE-KMS using the **AWS-managed key** (`sse_algorithm = "aws:kms"`, `aws/s3` — no custom CMK). Defense-in-depth (B + C3 already remove both private keys from state); adds a `kms:Decrypt` gate with nothing to manage. |
| **C2** IMDSv2 required | [modules/wireguard/main.tf:17-41](../../../terraform/modules/wireguard/main.tf#L17) | Add `metadata_options { http_tokens = "required", http_put_response_hop_limit = 1, http_endpoint = "enabled" }` to the launch template. |
| **C3** Remove SSH | [sg.tf](../../../terraform/modules/wireguard/sg.tf) + [private_key.tf](../../../terraform/modules/wireguard/private_key.tf) + [main.tf:21](../../../terraform/modules/wireguard/main.tf#L21) | Delete the port-22 ingress rule; delete `private_key.tf` in full (`tls_private_key.ssh`, `aws_key_pair.ssh`, `aws_ssm_parameter.ssh_private_key`); remove `key_name` from the launch template; remove `var.preconfigured_ssh_key_id`. Access is SSM Session Manager only. |
| **C4** Root-EBS encryption | [modules/wireguard/main.tf:17-41](../../../terraform/modules/wireguard/main.tf#L17) | Add `block_device_mappings { device_name=… ebs { encrypted = true } }` to the launch template. |
| **C5** Health-bucket posture | [modules/wireguard/main.tf:10-15](../../../terraform/modules/wireguard/main.tf#L10) | Add public-access-block + SSE + versioning, mirroring [client_store.tf](../../../terraform/modules/wireguard/client_store.tf). |
| **C6a** No-op ignore_changes | [modules/wireguard/main.tf:55](../../../terraform/modules/wireguard/main.tf#L55) | Remove `ignore_changes = [user_data, user_data_base64]` (instance uses `launch_template`, not inline user_data). |
| **C6b** Orphan SG | [modules/network/vpc/main.tf:53-76](../../../terraform/modules/network/vpc/main.tf#L53) | Remove the unattached world-open `aws_security_group.this` (not referenced by the wireguard instance). |
| **C6c** `isNoSuchKey` | [clientstore/s3store.go:135-137](../../../dashboard/internal/clientstore/s3store.go#L135) | Key the "object absent" classification on the `NoSuchKey` error code; when only the bare `"404"` fallback matches, log the raw stderr at WARN. |
| **C6d** Dead sentinel | [db/db.go:928-932](../../../dashboard/internal/db/db.go#L928) | Remove the unused exported `ErrNotInitialised`. |
| **C6e** Object-lock flag | [dev/backend/main.tf](../../../terraform/dev/backend/main.tf) | Either add a retention rule or drop `object_lock_enabled = true` (currently implies protection it doesn't enforce). |

---

## 3. Impact and Risk Analysis

**System dependencies:** the dashboard API (specs 004/015), the `clientstore`/S3 path, the poller, IMDSv2, the instance IAM role, and SSM Parameter Store.

**Potential risks & mitigations:**
- **IAM decouple recreating the role** — de-counting a `count = 1` resource changes its address and, without a `moved` block, destroys+recreates it, detaching the profile from the live instance. *Mitigation:* `moved` blocks for all five resources; verify the plan shows moves, not replacements.
- **Instance replacement from C2/C4** — `metadata_options` and `block_device_mappings` can't be changed on a running instance, so the launch-template change likely **replaces** the instance. *Mitigation:* this is exactly why B lands first/together — a rebuilt instance reads the same key back from SSM, so client configs survive. Call the replacement out in the plan review; expect one instance replace, no client breakage.
- **First-boot ordering** — AWS CLI must install before the SSM calls; the private-key `put-parameter` must be fatal before the `.ready` signal so a missing grant surfaces. *Mitigation:* explicit ordering + hard-fail in the wrapper.
- **SSH removal = SSM-only access** — no SSH fallback if IAM Identity Center is unavailable. *Mitigation:* accepted for a solo test VPN; keep an independent account-admin path (documented, not in repo).
- **IMDSv2 required** — could break metadata reads if any were v1. *Mitigation:* the wrapper already uses the IMDSv2 token flow ([user-data.txt:26-31](../../../terraform/modules/wireguard/templates/user-data.txt#L26)); the dashboard runs as a host process so hop-limit 1 is fine.
- **State-bucket KMS** — applies in the `backend/` bootstrap root (separate state). Using the AWS-managed `aws/s3` key means there is no CMK to create/manage; the SSE change re-encrypts on next write and gates reads behind `kms:Decrypt`. Low residual risk since B + C3 already remove all private keys from state.
- **`storeReady` recheck cadence** — the recheck shells out to `aws`; keep it on the coarse 1h retention tick, never a tight loop.

---

## 4. Testing Strategy

- **Go (A):** unit test for A1 — a DB-only (UI-added) client reaches both detail and history (the current tests only wire a seeded `clientsfileSvc` + empty `clientsSvc`, so this is a coverage gap). Unit test `RecheckStore` (latched→recovers on Load success and on ErrNotFound; no-op when ready/Noop). Test `/api/health` includes `client_store_ready` only in cloud mode. Add an `isNoSuchKey` test for the code-vs-substring distinction. `go build/vet/test ./...` + `gofmt`, static arm64 build.
- **Terraform (B/C):** `terraform fmt -recursive` + `make pre-commit` (fmt/docs/tflint/trivy). `validate` in both `terraform/dev/` and `terraform/dev/backend/`. Review `plan -out=tfplan`: confirm the IAM resources show as **moved** (not replaced), confirm at most one **instance replacement** (from C2/C4) and no other surprises, confirm the two new SSM params appear with the sentinel value, and confirm no server/SSH private-key material appears in the plan.
- **Owner E2E (can't be proven in-session — must run on real AWS):** fresh deploy with **no** manual SSM key → WireGuard comes up, public key present in SSM + printed by the installer; rebuild the instance → same server identity, existing client configs still connect; `aws ssm start-session` gives a shell; SSH to :22 is refused; a UI-added client's detail + history render; revoke S3 access to force `storeReady=false`, confirm the About badge/health field shows offline, restore access, confirm recovery within one retention tick **without** a restart or a peer edit.
