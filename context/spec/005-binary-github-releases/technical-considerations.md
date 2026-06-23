# Technical Specification: Dashboard Binary via GitHub Releases

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Replace the **build → S3 → SSM-deploy** pipeline with **tag → GitHub Release → boot-time pull**. Three surfaces change and several resources are deleted:

1. **CI** — the build workflow becomes a release workflow triggered by `vX.Y.Z` tags; it builds, tests, and publishes a GitHub Release with the binary + `SHA256SUMS`. It no longer authenticates to AWS.
2. **Terraform / user-data** — a new pinned `dashboard_release_tag` in `locals.tf` is threaded into user-data; the boot script fetches the release asset over HTTPS and verifies its checksum instead of `aws s3 cp`.
3. **Decommission** — the S3 artifacts bucket, the instance's `s3:GetObject` grant, the SSM deploy document, the deploy CI workflow, and the deploy IAM role are removed.

The release build is repo `vkatrichenko/wireguard-vpn`; the asset URL pattern is `https://github.com/vkatrichenko/wireguard-vpn/releases/download/<tag>/wireguard-dashboard`.

**Rollout model — replace-on-bump.** The pinned `dashboard_release_tag` is threaded into `local.user_data`, which is rendered into the **launch template** (`aws_launch_template.wireguard`, `terraform/modules/wireguard/main.tf:40`), not inline on the instance. Bumping the tag changes `local.user_data` → the launch template publishes a new version → `aws_instance.wireguard` (which references `launch_template.version = aws_launch_template.wireguard.latest_version`) sees a changed version and is **replaced**, with `create_before_destroy = true` standing up the new instance before tearing down the old. The EIP re-associates after the health check (existing module behavior), so the rollout is: bump tag → `apply` → new instance on the new version.

This is safe from "constant drift" because **`local.user_data` is deterministic** (`terraform/modules/wireguard/locals.tf:24-36`): its inputs are static vars, stable resource IDs, the `clients_config`, and the SSM key value — no `timestamp()`/`uuid()`/`random`. It changes only on a real input change (version bump, client edit, key rotation), so the instance replaces only then, not on every plan.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Architecture Changes — rollout mechanism (launch template)

The instance is provisioned via a **launch template**, so the rollout works through the LT version, not the instance's (unset, inline) `user_data`:

- `aws_launch_template.wireguard` holds `user_data = base64encode(local.user_data)` (`main.tf:40`). Threading `dashboard_release_tag` into the template means a tag bump re-renders `local.user_data` and publishes a **new LT version**.
- `aws_instance.wireguard` references `launch_template { version = aws_launch_template.wireguard.latest_version }` (`main.tf:48-51`). When `latest_version` advances, the instance is **replaced**, with `create_before_destroy = true` (`main.tf:54`) standing up the new instance first. The EIP re-associates after the health check (existing module behavior).
- **The `lifecycle { ignore_changes = [user_data, user_data_base64] }` on `aws_instance` (`main.tf:55`) is a no-op for this design** — those attributes aren't set on the instance (user-data lives on the LT). Leave it as-is; it neither enables nor blocks replace-on-bump. **No lifecycle edit is required.**

No "constant drift" risk: **`local.user_data` is deterministic** (`terraform/modules/wireguard/locals.tf:24-36`) — its inputs are static vars, stable resource IDs, the `clients_config`, and the SSM key value, with no `timestamp()`/`uuid()`/`random`. It changes only on a real input change (version bump, client edit, key rotation), so the instance replaces only then, not on every plan.

Instance replacement is acceptable here: the instance is effectively stateless — SQLite metrics are ephemeral/rebuildable, the server key is read from SSM at boot, and the EIP re-associates post-health-check. **Verify in `terraform plan`** that a tag change shows exactly one instance replacement plus a new LT version and nothing else stateful; confirm LT-version replacement semantics against the pinned AWS provider (`= 6.41.0`) via the Terraform MCP if uncertain.

> Today the binary version is *not* in user-data (it's the S3 `latest/` pointer, hot-swapped via SSM), which is why user-data is currently stable and dashboard updates don't replace the instance. Moving the tag into user-data deliberately makes each version bump a single, reviewable instance replacement — the intended trade for dropping SSM.

### CI / Release Workflow

- **Rename/replace** `.github/workflows/dashboard-build.yml` with a release workflow; **delete** `.github/workflows/dashboard-deploy.yml`.
- **Trigger:** `on: push: tags: ['v*.*.*']`.
- **Steps:** checkout → set up Go → `make test` (in `dashboard/`) → build static `linux/amd64` with existing ldflags (`-X main.BuildSHA/BuildTime/GoVersion`) plus the release tag → generate `SHA256SUMS` → create the GitHub Release for the tag and upload the binary + `SHA256SUMS`.
- **Auth:** uses the workflow's `GITHUB_TOKEN` with `permissions: contents: write` for release creation. **No AWS OIDC, no AWS credentials.**
- **Removed:** the S3 upload step, the build role's S3 permissions, the OIDC assume-role for build (unless still needed for nothing — drop it).

### Terraform / User-Data Changes

| File | Change |
|------|--------|
| `terraform/dev/locals.tf` | Add a pinned `dashboard_release_tag = "vX.Y.Z"` (sits with the pinned AMI; the value is the single source of truth for the running version). |
| `terraform/dev/main.tf` / module inputs | Thread `dashboard_release_tag` (and the repo `owner/name`, or a full base URL) into the `wireguard` module as a variable. |
| `terraform/modules/wireguard/variables.tf` | New variable(s): `dashboard_release_tag` (and release URL base / repo slug). Remove `dashboard_artifact_bucket_name`. |
| `terraform/modules/wireguard/templates/user-data.txt` | Replace the `aws s3 cp s3://${dashboard_artifact_bucket_name}/latest/...` line (~line 114) with: `curl -fsSL <release-url>/wireguard-dashboard -o <dst>` **and** download `SHA256SUMS`, then verify with `sha256sum -c` before `chmod +x`. Keep the `awscli` install + IMDSv2 + S3 health-check `.ready` signaling untouched. |
| `terraform/modules/wireguard/main.tf` | Resolve the `ignore_changes` decision above. |
| IAM (wireguard module) | Remove the instance role statement granting `s3:GetObject` on the artifacts bucket. Keep the health-check bucket grant. |
| S3 (wherever the artifacts bucket is defined) | Remove the `wireguard-vpn-test-dashboard-artifacts` bucket, its versioning, lifecycle, and policy resources. |
| SSM | Remove the `tf-wireguard-vpn-test-dashboard-deploy` document and the deploy CI IAM role (`tf-github-actions-dashboard-ci-deploy`). |

### Integrity Verification (user-data)

- Download `wireguard-dashboard` and `SHA256SUMS` from the same release tag.
- `sha256sum -c SHA256SUMS` (filtered to the binary) **must pass** before install; a failure aborts provisioning with a non-zero exit so the readiness signal is never written.
- `curl -f` ensures HTTP errors (e.g. a missing asset / private-repo 404) fail the script rather than writing an HTML error page to the binary path.

---

## 3. Impact and Risk Analysis

- **System Dependencies:**
  - **Repository must be public** for anonymous asset download — the hard precondition. Until then the user-data `curl` 404s; the spec is blocked on the repo-visibility change.
  - GitHub Releases availability is now in the **boot path** (vs. AWS S3 before). A GitHub outage during an instance launch would fail provisioning. Accepted trade-off for the open-source goal; mitigated by the fact that launches are infrequent and the running instance is unaffected by GitHub downtime.
  - The pinned AWS provider (`= 6.41.0`) and `terraform = 1.14.8` — confirm launch-template-version replacement semantics via the Terraform MCP if uncertain.

- **Potential Risks & Mitigations:**
  - **Update downtime.** A version bump replaces the instance (new LT version), so there's a brief outage during boot + EIP re-association. *Mitigation:* `create_before_destroy` brings up the replacement first; updates are operator-initiated and infrequent; EIP re-association already happens after the health check.
  - **Unintended replacement from unrelated user-data edits.** Any change to `local.user_data` (e.g. editing `clients_config`) also rolls a new LT version and replaces the instance. *Mitigation:* `local.user_data` is deterministic so this only fires on a real edit; the `plan -out` → review workflow surfaces the replacement before apply. This is the same trade as AMI pinning — deliberate and reviewable.
  - **Supply-chain / tamper.** A public URL fetch is unauthenticated. *Mitigation:* SHA256 verification against the release's `SHA256SUMS`; note cosign/minisign signing as a future hardening step (out of scope here).
  - **Destroy-safety.** Removing the artifacts bucket, IAM, and SSM doc are deletions. *Mitigation:* none of these are stateful VPN infra (no EIP, no state bucket, no key); per repo policy run a cross-resource check, confirm the bucket holds only rebuildable CI artifacts, and let the **owner** run the apply. Do not `-target`-destroy anything.
  - **Rollback.** *Mitigation:* re-pin an earlier `vX.Y.Z` and apply — the immutable, public release list makes any prior version reproducible (an improvement over the mutable S3 `latest/` pointer).
  - **Version drift between binary and metadata.** *Mitigation:* inject the release tag via ldflags so the running binary self-reports the exact tag, verifiable on the About tab.

---

## 4. Testing Strategy

- **Release workflow:** validate on a throwaway pre-release tag (e.g. `v0.0.1-rc1`) in a fork or with the repo already public — confirm the binary + `SHA256SUMS` attach, the binary self-reports the tag, and the job uses no AWS credentials.
- **Checksum path:** unit-test (or shell-test) the verify logic — a tampered/truncated binary must fail `sha256sum -c` and abort with non-zero exit.
- **Terraform:** `terraform validate` + `terraform plan` in `terraform/dev/` after the changes; **read the plan carefully** — confirm a tag bump shows exactly one instance replacement plus a new launch-template version, the artifacts bucket / IAM / SSM resources show as destroys, and nothing else stateful (EIP, state bucket, health-check bucket) is touched. Share the plan summary per the repo's "before claiming done" rule.
- **End-to-end (must be performed, not assumed):** apply to a real environment, confirm the instance boots, pulls the pinned tag, passes checksum, starts `wireguard-dashboard.service`, and the About tab shows the expected tag. A green plan does **not** prove the binary fetch works — the boot-time `curl` + checksum must be observed in `/var/log/cloud-init-output.log`.
- **Quality gate:** `make pre-commit` at repo root (fmt + docs + tflint + trivy) for the Terraform changes.
