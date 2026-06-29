# Tasks: ARM64 / AMD64 Architecture Option

_Vertical slices, ordered by the hard dependency: the dual-arch dashboard release must exist before the Terraform default flips to arm64. Each slice leaves the repo in a coherent, reviewable state._

---

### Slice 1 — Dual-arch dashboard release (ship first; nothing else boots without it)

- [x] Add an `arm64` cross-compile alongside the existing `amd64` step in `.github/workflows/dashboard-release.yml`, outputting `wireguard-dashboard-amd64` / `wireguard-dashboard-arm64` (same ldflags, `CGO_ENABLED=0`). **[Agent: cicd-github-actions]**
- [x] Update the `SHA256SUMS` step to checksum both binaries, and the `gh release create` step to publish all three assets. **[Agent: cicd-github-actions]**
- [x] Verify: `actionlint`/YAML-lint the workflow; dry-run the build steps locally (`GOOS=linux GOARCH=arm64 go build …` from `dashboard/`) to confirm a clean arm64 cross-compile and that `sha256sum -c` passes for each. **[Agent: go-fullstack]**
- [ ] **Owner-run:** push a prerelease tag, confirm the run publishes both arch assets + `SHA256SUMS`. (Workflow only triggers on a real tag push.)

### Slice 2 — Arch-agnostic boot (backward-compatible on amd64)

- [x] In `terraform/modules/wireguard/templates/user-data.txt`, add a `uname -m` detect block (`ARCH`=x86_64/aarch64 → `GOARCH`=amd64/arm64; unknown → `exit 1`). **[Agent: linux-cloud-init]**
- [x] Point the AWS CLI install at `awscli-exe-linux-${ARCH}.zip` and the dashboard download at `wireguard-dashboard-${GOARCH}`, installing to the unchanged `…/bin/wireguard-dashboard` path. Leave the `sha256sum -c --ignore-missing` verify as-is. **[Agent: linux-cloud-init]**
- [x] Verify: `shellcheck` the rendered template; confirm by inspection that the amd64 path is unchanged (uname=x86_64 → amd64 assets), so this slice is non-breaking against the Slice-1 release. **[Agent: linux-cloud-init]**

### Slice 3 — Terraform single toggle + arm64 default (the flip)

- [ ] Add `cpu_architecture` (default `"arm64"`), the `arch_config` map, and the derived `instance_type` (override-able) to `terraform/dev/locals.tf`, with map-index validation (invalid value fails plan). **[Agent: terraform-aws]**
- [ ] Drive `datasource.tf` AMI name suffix + `architecture` filter from `arch_config`, and pass `instance_type = local.instance_type` into the module in `main.tf`. Bump `dashboard_release_tag` to a tag built by Slice 1. **[Agent: terraform-aws]**
- [ ] Verify (agent, read-only): `terraform fmt -recursive`; `AWS_PROFILE=csm aws ec2 describe-images …` to confirm the arm64 Ubuntu 24.04 AMI actually resolves; `make pre-commit`. **[Agent: terraform-aws]**
- [ ] **Owner-run:** `terraform validate` + `terraform plan -out=tfplan` in `terraform/dev/`; confirm the diff is exactly the AMI + instance replacement and the AMI is arm64. Flip to `x86_64` and re-plan to prove the toggle is symmetric.

### Slice 4 — End-to-end arm64 validation (owner-run; cannot be done in-session)

- [ ] **Owner-run:** `terraform apply tfplan` on arm64; SSM in; check `/var/log/cloud-init-output.log` for a clean boot; `systemctl status wireguard-dashboard` active; client WireGuard handshake; dashboard reachable over the tunnel with all tabs working.

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 1 verify | `actionlint` / `shellcheck` may not be installed locally | Agent falls back to manual inspection; install for stronger checks |
| Slice 1 owner-run | Release workflow only triggers on a real tag push (GHA) | Owner pushes the prerelease tag — not automatable per repo rules |
| Slice 3 / 4 | `terraform plan`/`apply` are owner-run only (CLAUDE.md) | Agent does fmt/validate/describe-images; owner runs plan + apply |
| Slice 4 | E2E boot/handshake needs a live apply + SSM | Owner-run; "done" not claimed on boot behavior without it |
