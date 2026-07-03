# Tasks: Hardening & Server-Key Automation

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)
- **Status:** In Progress

---

> **Design recap:** Three independent workstreams. **A** — dashboard: repoint client detail/history to the runtime DB, and give the cloud-mode S3 backup a self-heal recheck + a health signal. **B** — the instance self-manages its WireGuard server key via SSM (reuse-or-generate at boot), so the key never enters Terraform state or the launch template; Terraform owns the SSM param shell (sentinel + `ignore_changes`); the public key is published to a String param + installer stdout. **C** — an infra hardening batch (IMDSv2, root-EBS encryption, SSH removal → SSM-only, state-bucket AWS-managed KMS, health-bucket posture, dead-config cleanup).
>
> **Ordering constraints (load-bearing):** Slice 2 (decouple the IAM role from `use_eip`) **must precede** Slice 3 (which adds SSM key grants to that role). Slice 3 (server-key automation) **must precede** Slice 5's instance-replacing changes (IMDSv2 / root-EBS) so a rebuilt instance reads the same key back from SSM instead of breaking every client config.
>
> **Verification reality:** No live `terraform plan`/`apply` in-session (`csm` SSO expired — owner runs it). No browser MCP — dashboard verified via Go build/tests + owner E2E. SSM/EC2/S3 live behavior is owner-run (Slice 6). Terraform resource **moves** (Slice 2) and the single expected **instance replacement** (Slice 5) are confirmed by the owner in the plan diff.
>
> **Per-slice gate:** Terraform → `terraform fmt -recursive` + `make pre-commit` (fmt/docs/tflint/trivy), and use the `terraform` MCP to confirm provider schemas before writing resources; `install.sh` / user-data wrapper → `make shellcheck`; dashboard → `cd dashboard && go build ./... && go vet ./... && go test ./...` + `gofmt -l .` + static `CGO_ENABLED=0 GOOS=linux GOARCH=arm64` build.

---

## Slice 1 — Dashboard: fix UI-added-client detail/history + backup health/self-heal + Go tidies (req A1, A2, C6c, C6d)

- [ ] Client detail & history resolve against the runtime DB, and the cloud-mode backup exposes + self-heals its health **[Agent: go-fullstack]**
  - [ ] **A1:** repoint `handleGetPartialClientDetail` ([handlers_clients.go:151](../../../dashboard/internal/server/handlers_clients.go#L151)) and `handleGetClientHistory` ([:302](../../../dashboard/internal/server/handlers_clients.go#L302)) to resolve name→client via `s.clientsSvc.List(ctx)` (the pattern `handleGetClientConfig` already uses, [handlers_config.go:41-59](../../../dashboard/internal/server/handlers_config.go#L41)); drop the `clientsfileSvc.Load()` lookup
  - [ ] **A2:** add `Service.RecheckStore(ctx) error` to `internal/clients/service.go` — a Load-only probe reusing the `saveStoreLocked` self-heal branch ([service.go:485-508](../../../dashboard/internal/clients/service.go#L485)): under lock, when `storeReady==false`, `store.Load`; on success or `ErrNotFound` → `setStoreReady(true)`; no-op when ready or `NoopStore`
  - [ ] **A2:** hook `RecheckStore` onto the poller's hourly retention tick (`DefaultRetentionEvery`, [poller.go:73](../../../dashboard/internal/poller/poller.go#L73)) via a small `StoreRechecker` interface passed into `poller.New` (nil/Noop in local mode → tick skips it); no new ticker
  - [ ] **A2:** make `handleHealth` a method `s.handleHealth` ([server.go:295](../../../dashboard/internal/server/server.go#L295)); add `"client_store_ready": bool` to the JSON **only** when `clientManagementMode=="cloud"`
  - [ ] **A2:** add a client-store health row to the About view (`web/templates/tabs/about.html` / `cards/about-*.html`), cloud-only, fed by `clientsSvc.StoreReady()`
  - [ ] **C6c:** in `isNoSuchKey` ([s3store.go:135-137](../../../dashboard/internal/clientstore/s3store.go#L135)) key on the `NoSuchKey` error code; when only the bare `"404"` fallback matches, log raw stderr at WARN
  - [ ] **C6d:** remove the unused exported `ErrNotInitialised` ([db.go:928-932](../../../dashboard/internal/db/db.go#L928))
  - [ ] Tests: a DB-only (UI-added) client reaches both detail and history (current tests only wire a seeded `clientsfileSvc` + empty `clientsSvc` — coverage gap); `RecheckStore` latched→recovers on Load-success and on `ErrNotFound`, no-op when ready/Noop; `/api/health` carries `client_store_ready` only in cloud mode; `isNoSuchKey` code-vs-substring
  - [ ] Verify: dashboard gate green (build/vet/test + gofmt + static arm64); confirm `/api/health` shape in both modes and the About badge renders

## Slice 2 — Terraform: decouple the instance IAM role/profile from `use_eip` (req B4)

- [ ] The instance role, profile, and baseline grants exist unconditionally; only EIP-association stays EIP-gated **[Agent: terraform-aws]**
  - [ ] Remove `count = var.use_eip ? 1 : 0` from `aws_iam_policy.wireguard_policy`, `aws_iam_role.wireguard_role`, both `aws_iam_role_policy_attachment.*`, and `aws_iam_instance_profile.wireguard_profile` ([iam.tf:71-108](../../../terraform/modules/wireguard/iam.tf#L71)); update the `[0]` index references (attachments, profile, and the launch-template instance-profile ref)
  - [ ] Move the `ec2:AssociateAddress` statement ([iam.tf:15-21](../../../terraform/modules/wireguard/iam.tf#L15)) into `dynamic "statement" { for_each = var.use_eip ? [1] : [] }` — the only EIP-conditional grant
  - [ ] Add `moved` blocks for each de-counted resource (`...[0]` → `...`) so Terraform records address moves, **not** destroy/recreate
  - [ ] Verify: `terraform fmt -recursive` + `make pre-commit` green; **owner** confirms the plan shows the five resources as *moved*, not replaced, and the instance keeps its profile

## Slice 3 — Server-key automation: instance self-manages the key; remove the TF read path (req B1, B2, B3)

- [ ] Terraform owns two SSM param shells + the IAM grants; the AWS user-data wrapper resolves/generates/publishes the key **[Agent: terraform-aws]** **[Agent: linux-cloud-init]**
  - [ ] **TF (terraform-aws):** add `aws_ssm_parameter.wg_server_private_key` (SecureString, `alias/aws/ssm`, `value="UNINITIALIZED"`, `lifecycle { ignore_changes=[value] }`) and `aws_ssm_parameter.wg_server_public_key` (String, same treatment) — e.g. a new `server_key.tf`; names derived from `${project}-${env}`
  - [ ] **TF (terraform-aws):** add IAM statements to `wireguard_policy_doc` — `ssm:GetParameter`+`ssm:PutParameter` on the private-key ARN, `ssm:PutParameter` on the public-key ARN (no `kms:` statement — AWS-managed key)
  - [ ] **TF (terraform-aws):** remove the read path — `data.aws_ssm_parameter.wg_server_private_key` ([datasource.tf:1-3](../../../terraform/modules/wireguard/datasource.tf#L1)), `local.wg_server_private_key` ([locals.tf:71](../../../terraform/modules/wireguard/locals.tf#L71)) + its templatefile arg, `var.wg_server_private_key_param` ([variables.tf:87](../../../terraform/modules/wireguard/variables.tf#L87)), and the root assignment ([dev/main.tf:21](../../../terraform/dev/main.tf#L21)); thread the two param **names** into the templatefile map
  - [ ] **Wrapper (linux-cloud-init):** in `templates/user-data.txt` — move the AWS CLI install *before* the key step; **pre-install** `aws ssm get-parameter --with-decryption` → `export WG_SERVER_PRIVATE_KEY` only when the value is a valid 44-char key (else leave empty so `install.sh` generates); remove the `WG_SERVER_PRIVATE_KEY="${wg_server_private_key}"` export line
  - [ ] **Wrapper (linux-cloud-init):** **post-install** publish — if SSM had no real key, `put-parameter --overwrite` the private key (SecureString) read from `/etc/wireguard/server.key`; always `put-parameter --overwrite` the public key (`wg pubkey < server.key`, String). A failed private-key put is **fatal before** the S3 `.ready` signal
  - [ ] Confirm `install.sh` is unchanged for key logic (its `env → server.key → genkey` precedence + pubkey stdout already satisfy the reuse/generate/print contract)
  - [ ] Verify: `terraform fmt -recursive` + `make pre-commit` + `make shellcheck` green; reason through both boot paths (SSM-present → reuse; SSM-absent → generate+publish); confirm no private-key value remains in the rendered user-data or plan

## Slice 4 — Terraform: remove SSH; SSM Session Manager is the only shell path (req C3)

- [ ] Public SSH and all SSH key material are deleted; management access is SSM-only **[Agent: terraform-aws]**
  - [ ] Delete the port-22 ingress rule in [sg.tf](../../../terraform/modules/wireguard/sg.tf)
  - [ ] Delete [private_key.tf](../../../terraform/modules/wireguard/private_key.tf) in full (`tls_private_key.ssh`, `aws_key_pair.ssh`, `aws_ssm_parameter.ssh_private_key`); remove `key_name` from the launch template ([main.tf:21](../../../terraform/modules/wireguard/main.tf#L21)); remove `var.preconfigured_ssh_key_id`
  - [ ] Verify: `terraform fmt -recursive` + `make pre-commit` green; **owner** confirms the plan destroys the SSH keypair/param (not the instance) and that the SSH PEM no longer appears in state; docs (README) drop any SSH-access mention in favor of `aws ssm start-session`

## Slice 5 — Terraform: infra hardening batch (req C1, C2, C4, C5, C6a, C6b, C6e)

- [ ] Metadata, encryption, and bucket posture hardened; dead/misleading config removed **[Agent: terraform-aws]**
  - [ ] **C2:** add `metadata_options { http_tokens="required", http_put_response_hop_limit=1, http_endpoint="enabled" }` to the launch template ([main.tf:17-41](../../../terraform/modules/wireguard/main.tf#L17))
  - [ ] **C4:** add `block_device_mappings { ebs { encrypted = true } }` to the launch template
  - [ ] **C5:** give the `health_check` bucket ([main.tf:10-15](../../../terraform/modules/wireguard/main.tf#L10)) public-access-block + SSE + versioning, mirroring [client_store.tf](../../../terraform/modules/wireguard/client_store.tf)
  - [ ] **C6a:** remove the no-op `ignore_changes = [user_data, user_data_base64]` on `aws_instance` ([main.tf:55](../../../terraform/modules/wireguard/main.tf#L55))
  - [ ] **C6b:** remove the unattached world-open `aws_security_group.this` in [network/vpc/main.tf:53-76](../../../terraform/modules/network/vpc/main.tf#L53)
  - [ ] **C1:** switch the state bucket ([dev/backend/main.tf](../../../terraform/dev/backend/main.tf)) SSE-S3 → SSE-KMS with the **AWS-managed** key (`sse_algorithm="aws:kms"`, `aws/s3` — no custom CMK)
  - [ ] **C6e:** add a retention rule to, or drop, `object_lock_enabled = true` on the state bucket so it no longer implies unenforced protection
  - [ ] Verify: `terraform fmt -recursive` + `make pre-commit` green in both `terraform/dev/` and `terraform/dev/backend/`; **owner** reviews the plan — expect **one** instance replacement (from C2/C4; safe because Slice 3 makes the rebuild key-stable) and no other surprises

## Slice 6 — Owner-run live end-to-end validation (cannot be done in-session)

- [ ] **Owner-run** (after `aws sso login --profile csm`, one dashboard release with the Slice-1 binary): fresh AWS deploy with **no** manual SSM key → WireGuard comes up, public key present in the SSM String param and printed by the installer; **rebuild the instance** → same server identity, existing client configs still connect; `aws ssm start-session --target <id>` gives a shell; SSH to :22 is refused; a **UI-added** client's detail + history render; force `storeReady=false` (revoke S3 access) → the About badge / `/api/health` shows offline, restore access → recovers within one retention tick **without** a restart or a peer edit. **(owner)**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 2 | No live `terraform plan` in-session (expired `csm` SSO) | Agent runs `fmt` + `make pre-commit`; **owner** confirms moves-not-replacements in the plan |
| Slice 3 | Wrapper split across TF + bash; live SSM/IAM not reachable in-session | `terraform-aws` owns the HCL, `linux-cloud-init` owns `user-data.txt`; both boot paths reasoned through; live behavior in Slice 6 |
| Slice 5 | C2/C4 likely replace the instance | Intentional and safe post-Slice-3; **owner** verifies exactly one replacement in the plan |
| Slice 1 | No browser MCP for UI verification | Verify via Go build/tests + template render; visual confirm in owner E2E |
| Slice 6 | Live SSM/EC2/S3 not reachable in-session | Owner-run end-to-end |
