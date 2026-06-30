<!--
This document describes HOW to build the feature at an architectural level.
It is NOT a copy-paste implementation guide.
-->

# Technical Specification: ARM64 / AMD64 Architecture Option

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Three coordinated layers, shipped in dependency order:

1. **Release pipeline** (`.github/workflows/dashboard-release.yml`) — build **both** `amd64` and `arm64` dashboard binaries and publish them as arch-suffixed assets under one multi-arch `SHA256SUMS`. **Must ship + be tagged first.**
2. **Boot script** (`terraform/modules/wireguard/templates/user-data.txt`) — detect the running architecture at boot and select the matching AWS CLI installer and dashboard binary. No new `templatefile` variable.
3. **Terraform** (`terraform/dev/`) — a single `local.cpu_architecture` toggle drives the AMI name suffix, the AMI `architecture` filter, and the default instance type via a lookup map. Default `= "arm64"`.

No module interface changes beyond the value passed for `instance_type`; the module already parameterizes `ami_id` and `instance_type`.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Release pipeline — dual-arch build

`.github/workflows/dashboard-release.yml` (single job, linear steps — no matrix):

| Step | Change |
|---|---|
| Cross-compile | Duplicate the `GOARCH=amd64` build (lines 71–91) into a second `GOARCH=arm64` step (or a shell loop over `amd64 arm64`), each `-o wireguard-dashboard-<goarch>`. `CGO_ENABLED=0` already makes this a pure cross-compile; same ldflags. |
| SHA256SUMS | `sha256sum wireguard-dashboard-amd64 wireguard-dashboard-arm64 > SHA256SUMS` (line 97) — bare filenames, two entries. |
| Publish | `gh release create … wireguard-dashboard-amd64 wireguard-dashboard-arm64 SHA256SUMS` (line 113). |

**Asset rename consequence:** the single `wireguard-dashboard` asset becomes `-amd64`/`-arm64`. Release tags cut **before** this change have no arch-suffixed asset, so the new boot script can't boot them — recorded as the FS §2.3 prerequisite. The pinned `dashboard_release_tag` must be bumped to a post-change tag.

### 2.2 Boot script — runtime arch selection

`terraform/modules/wireguard/templates/user-data.txt`, after package install (~line 12):

- Add a detect block: `ARCH="$(uname -m)"` → `x86_64` | `aarch64`; map to `GOARCH` (`amd64` | `arm64`). Unknown value → fail hard (`exit 1`), consistent with the script's explicit-failure model.
- **AWS CLI** (line 14): `…/awscli-exe-linux-${ARCH}.zip` — AWS uses the `uname -m` spellings (`x86_64`, `aarch64`) directly.
- **Dashboard download** (line 160): fetch `${RELEASE_URL}/wireguard-dashboard-${GOARCH}` → temp dir; `install … -o …/bin/wireguard-dashboard` (line 178) keeps the final on-disk name unchanged so the systemd unit needs no edit.
- **Verification** (line 171): unchanged — `sha256sum -c --ignore-missing SHA256SUMS` already tolerates the two-entry file and checks only the downloaded binary.

No new `templatefile()` var; the template stays arch-agnostic.

### 2.3 Terraform — single toggle

`terraform/dev/locals.tf`:

| Local | Purpose |
|---|---|
| `cpu_architecture` | `"arm64"` (default) \| `"x86_64"` — the one knob. |
| `arch_config` (map) | `{ x86_64 = { ami_suffix="amd64", ami_arch="x86_64", default_instance_type="t3a.micro" }, arm64 = { ami_suffix="arm64", ami_arch="arm64", default_instance_type="t4g.micro" } }`. |
| `instance_type` | Optional explicit override; falls back to `arch_config[cpu_architecture].default_instance_type`. |

**Validation:** index the map directly — `local.arch_config[local.cpu_architecture]` — so an invalid value fails `plan` ("key … does not exist"). Optionally wrap with a `check` block naming the allowed values for a friendlier message. Keeps the knob in `locals.tf` (no `variable`/`tfvars`, per convention).

`terraform/dev/datasource.tf`:
- Name filter (line 7): `…-noble-24.04-${local.arch_config[local.cpu_architecture].ami_suffix}-server-*`.
- `architecture` filter (line 17): `[local.arch_config[local.cpu_architecture].ami_arch]`.

`terraform/dev/main.tf`: pass `instance_type = local.instance_type` into the `wireguard` module (currently unset → relies on module default). Module `variables.tf` default may be updated to `t4g.micro` for consistency, but `main.tf` now always passes it explicitly.

---

## 3. Impact and Risk Analysis

- **System dependencies:** AMI lookup (Canonical owner `099720109477` publishes Ubuntu 24.04 `arm64` server images — same pattern, `arm64` token); `aws_launch_template.image_id` + `aws_instance.instance_type` both change → **instance replacement** (expected). `aws_instance` has `ignore_changes = [user_data*]`, but replacement provisions the new box with current user-data, so the arch-aware script applies on the new instance.
- **Ordering risk (highest):** flipping the TF default to arm64 before a dual-arch release exists → new instance boots, dashboard binary download 404s, provisioning aborts (no `.ready`), dashboard absent. **Mitigation:** task order forces release+tag-bump first; the fail-hard boot + `cloud-init-output.log` surface it loudly rather than silently.
- **Parity:** `t4g.micro` = 2 vCPU / 1 GiB, matching `t3a.micro`; WireGuard is in-kernel on Ubuntu 24.04 arm64; CGO-free SQLite + `go:embed` assets cross-compile cleanly. Low functional risk.
- **Pre-existing drift (out of scope, noted):** `datasource.tf` uses `most_recent = true` despite CLAUDE.md's "pin AMIs explicitly." This change doesn't worsen it; explicit AMI pinning is a separate concern.

---

## 4. Testing Strategy

- **Static:** `terraform fmt -recursive`; `terraform validate` in `terraform/dev/`; `make pre-commit` (fmt/docs/tflint/trivy). Confirm `plan` for `arm64` shows only the AMI + instance replacement and the AMI resolves to an `arm64` Ubuntu 24.04 image.
- **Toggle regression:** flip `cpu_architecture = "x86_64"` and confirm `plan` resolves the amd64 AMI + `t3a.micro` — proves the toggle is symmetric. Confirm an invalid value fails `plan`.
- **Release dry-run:** push a prerelease tag; verify both `-amd64`/`-arm64` assets + `SHA256SUMS` exist and `sha256sum -c` passes for each binary.
- **E2E (owner-run, cannot be done in-session):** apply on `arm64`; SSM in; check `/var/log/cloud-init-output.log` for clean boot; `systemctl status wireguard-dashboard` active; WireGuard handshake from a client; dashboard reachable over the tunnel with all tabs functional. Per CLAUDE.md, "done" is not claimed on boot behavior without this.
