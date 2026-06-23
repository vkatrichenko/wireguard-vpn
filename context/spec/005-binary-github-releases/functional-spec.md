# Functional Specification: Dashboard Binary via GitHub Releases

- **Roadmap Item:** Not yet on the roadmap (open-sourcing prerequisite; replaces the private S3 + SSM distribution path)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The dashboard binary is currently a **private** artifact: CI builds it and pushes it to a private S3 bucket (`wireguard-vpn-test-dashboard-artifacts`), the EC2 instance pulls it with an IAM-scoped `s3:GetObject`, and updates are hot-swapped onto the running host via an SSM deploy document. None of this works for an open-source project — an external reader can't download the binary, can't verify it, and can't see a version history. It also ties the project to AWS-internal plumbing (a bucket, two IAM roles, an SSM document) that has nothing to do with WireGuard.

This change makes the binary a **public, versioned, verifiable GitHub Release** and makes the running version an **explicit, pinned, committed choice** — the same philosophy this repo already applies to AMIs ("AMI rotation becomes an explicit, reviewable commit"). The instance fetches a pinned release tag at boot from the public release URL; bumping the version is a one-line commit + apply, not an out-of-band SSM push.

This removes a whole class of moving parts (S3 artifact bucket, instance S3 IAM grant, SSM deploy document, the CI deploy role) and gives anyone — including future open-source users — a clean `Download` page with checksums.

**Precondition:** GitHub Release assets are only anonymously downloadable on a **public** repository. This spec assumes the repository is (or is being made) public; until then the instance fetch would still need a token, which defeats the simplification. Making the repo public is tracked as a dependency, not part of this spec's code.

**Success looks like:** a new contributor downloads `wireguard-dashboard` for a tagged release from the GitHub Releases page, verifies its SHA256, and sees exactly which version their VPN host runs — and the operator updates the host by bumping one pinned tag in `locals.tf` and running `terraform apply`, with no SSM, no S3, and no IAM in the path.

---

## 2. Functional Requirements (The "What")

### 2.1 Public versioned releases

- **As an open-source user, I want to** download a specific dashboard version from GitHub Releases, **so that I can** run or inspect it without AWS access.
  - **Acceptance Criteria:**
    - [ ] Pushing a SemVer tag `vX.Y.Z` publishes a GitHub Release for that tag.
    - [ ] The release attaches the built `wireguard-dashboard` binary (Linux/amd64) as a downloadable asset.
    - [ ] The release attaches a `SHA256SUMS` file covering the binary.
    - [ ] The binary reports its version: invoking it (or an existing version field/endpoint) shows the release tag plus the existing build SHA / build time / Go version metadata.
    - [ ] Releases are anonymously downloadable (depends on the repo being public — see precondition).

### 2.2 Tag-driven release process

- **As the maintainer, I want** releases cut from SemVer tags only, **so that** the release list stays deliberate and human-readable.
  - **Acceptance Criteria:**
    - [ ] Only `vX.Y.Z` tag pushes produce a release; pushes to `main` do **not** publish or deploy anything.
    - [ ] The release build runs `make test` (or equivalent) and fails the release if tests fail.
    - [ ] The release build requires **no AWS credentials** — it neither reads nor writes any AWS resource.

### 2.3 Pinned version on the instance

- **As the operator, I want** the running dashboard version pinned in code, **so that** the deployed version is always exactly what's committed.
  - **Acceptance Criteria:**
    - [ ] The dashboard release tag is a single pinned value in `terraform/dev/locals.tf` (alongside the pinned AMI), threaded into user-data.
    - [ ] On boot, the instance downloads the pinned release's binary from the public GitHub release URL and **verifies its SHA256** against the published checksum before installing it.
    - [ ] If the download or checksum verification fails, provisioning fails loudly (the existing cloud-init log is the diagnostic surface) rather than starting a wrong or partial binary.
    - [ ] The instance fetch uses no AWS credentials and no `s3:GetObject` (that IAM grant is removed).

### 2.4 Updating the running version

- **As the operator, I want to** update the host by changing one committed value, **so that** version changes are reviewable and reversible.
  - **Acceptance Criteria:**
    - [ ] Updating the dashboard is: bump the pinned tag in `locals.tf` → `terraform plan` → `terraform apply`.
    - [ ] A version bump rolls a new launch-template version and **replaces the instance** (`create_before_destroy`); the operator reviews the replacement in `terraform plan` before applying, per the repo's apply workflow. Because `local.user_data` is deterministic, the plan is clean between bumps (no spurious drift).
    - [ ] Rolling back is the same operation with an earlier tag.
    - [ ] The Elastic IP and server identity survive the update (EIP re-associates; server key still comes from SSM), so clients reconnect without config changes.

### 2.5 Decommissioned distribution path

- **As the maintainer, I want** the old private path removed, **so that** there's one obvious way to ship the binary.
  - **Acceptance Criteria:**
    - [ ] The SSM-based deploy workflow and its SSM document are removed.
    - [ ] The S3 artifacts bucket and the instance's `s3:GetObject` grant on it are removed.
    - [ ] The CI deploy IAM role (and the build role's S3 permissions) are removed; CI retains only what a no-AWS release needs.
    - [ ] The S3 **health-check** bucket used for boot-readiness signaling is **retained** (it is unrelated to artifact distribution).

---

## 3. Scope and Boundaries

### In-Scope

- A tag-triggered CI workflow that builds, tests, and publishes a GitHub Release with the Linux/amd64 binary + `SHA256SUMS`.
- A pinned `dashboard_release_tag` in `locals.tf`, threaded into user-data.
- User-data fetching the pinned release over HTTPS with SHA256 verification (replacing `aws s3 cp`).
- Removal of: the S3 artifacts bucket, the instance S3 IAM grant, the SSM deploy document, the SSM-deploy CI workflow, and the deploy IAM role.
- A defined update/rollback workflow via Terraform: bump the pinned tag → new launch-template version → instance replacement (`create_before_destroy`, EIP re-associates).

### Out-of-Scope

- **Making the repository public** — a prerequisite tracked separately; this spec assumes it happens.
- **Multi-architecture / multi-OS builds** — Linux/amd64 only (the binary is EC2/Linux-specific: IMDSv2, `/proc`, `wg`, embedded GeoLite2). arm64/darwin builds are explicitly deferred.
- **Cryptographic signing** (cosign / minisign / GPG) — `SHA256SUMS` only for v1; signing is a possible follow-up.
- **In-place hot-swap updates** — deliberately replaced by the pinned-version model; no SSM, no on-host updater, no systemd auto-update timer.
- **Homebrew / apt / container-image distribution channels** — not in scope.
- **Changes to what the dashboard does at runtime** — this spec is purely about how the binary is built, published, fetched, and versioned.
- **Config download (004), connection-history/geo-map (006), alerts (007)** — separate specifications.
