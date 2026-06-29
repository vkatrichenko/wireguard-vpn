# Tasks: ARM64 / AMD64 Architecture Option

_Vertical slices, ordered by the hard dependency: the dual-arch dashboard release must exist before the Terraform default flips to arm64. Each slice leaves the repo in a coherent, reviewable state._

---

### Slice 1 — Dual-arch dashboard release (ship first; nothing else boots without it)

- [x] Add an `arm64` cross-compile alongside the existing `amd64` step in `.github/workflows/dashboard-release.yml`, outputting `wireguard-dashboard-amd64` / `wireguard-dashboard-arm64` (same ldflags, `CGO_ENABLED=0`). **[Agent: cicd-github-actions]**
- [x] Update the `SHA256SUMS` step to checksum both binaries, and the `gh release create` step to publish all three assets. **[Agent: cicd-github-actions]**
- [x] Verify: `actionlint`/YAML-lint the workflow; dry-run the build steps locally (`GOOS=linux GOARCH=arm64 go build …` from `dashboard/`) to confirm a clean arm64 cross-compile and that `sha256sum -c` passes for each. **[Agent: go-fullstack]**
- [x] **Owner-run:** push a tag, confirm the run publishes both arch assets + `SHA256SUMS`. _(Done — `v0.0.7` carries `wireguard-dashboard-amd64`, `-arm64`, `SHA256SUMS`.)_

### Slice 2 — Arch-agnostic boot (backward-compatible on amd64)

- [x] In `terraform/modules/wireguard/templates/user-data.txt`, add a `uname -m` detect block (`ARCH`=x86_64/aarch64 → `GOARCH`=amd64/arm64; unknown → `exit 1`). **[Agent: linux-cloud-init]**
- [x] Point the AWS CLI install at `awscli-exe-linux-${ARCH}.zip` and the dashboard download at `wireguard-dashboard-${GOARCH}`, installing to the unchanged `…/bin/wireguard-dashboard` path. Leave the `sha256sum -c --ignore-missing` verify as-is. **[Agent: linux-cloud-init]**
- [x] Verify: `shellcheck` the rendered template; confirm by inspection that the amd64 path is unchanged (uname=x86_64 → amd64 assets), so this slice is non-breaking against the Slice-1 release. **[Agent: linux-cloud-init]**

### Slice 3 — Terraform single toggle + arm64 default (the flip)

- [x] Add `cpu_architecture` (default `"arm64"`), the `arch_config` map, and the derived `instance_type` (override-able) to `terraform/dev/locals.tf`, with validation (map-index hard-errors on an invalid value; a `check` block adds a friendly message). **[Agent: terraform-aws]**
- [x] Drive `datasource.tf` AMI name suffix + `architecture` filter from `arch_config`, and pass `instance_type = local.instance_type` into the module in `main.tf`. **[Agent: terraform-aws]**
- [x] **(owner-gated)** Bump `dashboard_release_tag` to a dual-arch tag before any arm64 apply. _(Done — `main.tf` pins `v0.0.7`; the placeholder TODO is gone.)_
- [x] Verify (agent, read-only): `terraform fmt -recursive` ✓; `make pre-commit` ✓ (fmt/docs/tflint/trivy all Passed). `describe-images` for the arm64 AMI is **pending** — the `csm` SSO session is expired; re-run after `aws sso login --profile csm`. **[Agent: terraform-aws]**
- [x] **Owner-run:** `terraform plan`/`apply` in `terraform/dev/`. _(Done 2026-06-29 — default `apply` provisioned the arm64/t4g instance.)_

### Slice 3b — Refactor: AMI lookup + arch mapping into the module (supersedes Slice 3's dev-side placement)

_Post-Slice-3 cleanup: the arch→{AMI, instance-type} ownership moved out of the dev root and into the `wireguard` module, so the module is self-contained and arch-aware. `make pre-commit` green (fmt/docs/tflint/trivy)._

- [x] Module gains `cpu_architecture` var (with `validation`), the `arch_config` map, the `aws_ami` data source (count-gated on the `ami_id` override), and `effective_ami_id` / `effective_instance_type` locals. `ami_id` kept as explicit override; `instance_type` now an arch-derived optional override. **[Agent: terraform-aws]**
- [x] Dev root slimmed: `terraform/dev/datasource.tf` deleted, `arch_config` / `instance_type` derivation / `check` block removed from `locals.tf`; `main.tf` module call now passes only `cpu_architecture`. No dangling refs (grep clean). **[Agent: terraform-aws]**

### Slice 4 — End-to-end arm64 validation (owner-run; cannot be done in-session)

- [x] **Owner-run:** `terraform apply` on arm64; clean boot; client WireGuard handshake; dashboard reachable over the tunnel with all tabs working. _(Owner-verified 2026-06-29.)_

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 1 verify | `actionlint` / `shellcheck` may not be installed locally | Agent falls back to manual inspection; install for stronger checks |
| Slice 1 owner-run | Release workflow only triggers on a real tag push (GHA) | Owner pushes the prerelease tag — not automatable per repo rules |
| Slice 3 / 4 | `terraform plan`/`apply` are owner-run only (CLAUDE.md) | Agent does fmt/validate/describe-images; owner runs plan + apply |
| Slice 4 | E2E boot/handshake needs a live apply + SSM | Owner-run; "done" not claimed on boot behavior without it |
