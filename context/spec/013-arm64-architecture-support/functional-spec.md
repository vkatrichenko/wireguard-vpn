# Functional Specification: ARM64 / AMD64 Architecture Option

- **Roadmap Item:** "ARM option (Spec C)" (promoting from _Future / Under Consideration_)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The VPN host is currently locked to **x86_64**: the AMI lookup hardcodes `architecture = ["x86_64"]` and the `amd64` AMI-name pattern, the dashboard release ships an amd64-only binary, and the boot script hardcodes the x86_64 AWS CLI. An operator who wants to run on AWS **Graviton (`arm64`)** — for ~11% lower on-demand cost at the same size (`t4g.micro` vs `t3a.micro`) and better price/performance — has no supported path; it would mean editing four+ files by hand and still hitting a missing arm64 dashboard binary.

This change makes CPU architecture a **single, reviewable toggle** in `locals.tf` and flips the **default to `arm64`**, so a fresh deploy lands on cheaper Graviton out of the box while amd64 remains a one-line opt-in.

**Success looks like:** an operator sets `cpu_architecture = "arm64"` (or accepts the default) and a single `terraform apply` stands up a Graviton instance whose WireGuard tunnel and dashboard work identically to the x86_64 build; switching back to `"x86_64"` is the only change needed to return to Intel/AMD.

**Non-goal:** no change to VPN or dashboard _functionality_ — same features, same UI, same alerting — only the underlying CPU architecture becomes selectable.

---

## 2. Functional Requirements (The "What")

### 2.1 Single architecture toggle

- **As the operator, I want** to choose the host CPU architecture from one place, **so that** I don't have to hand-edit AMI filters, instance types, and boot scripts to switch.
  - **Acceptance Criteria:**
    - [ ] A single `cpu_architecture` setting in `terraform/dev/locals.tf` accepts `"x86_64"` or `"arm64"`; any other value fails `terraform plan` with a clear validation error.
    - [ ] The setting derives **all** of: the AMI name suffix (`amd64`/`arm64`), the AMI `architecture` filter (`x86_64`/`arm64`), and the **default** instance type (`x86_64`→`t3a.micro`, `arm64`→`t4g.micro`).
    - [ ] The instance type remains explicitly overridable for sizing (e.g. `t4g.small`) without touching the module.
    - [ ] No other file needs editing to change architecture.

### 2.2 Default is arm64

- **As the operator, I want** the default deploy to use Graviton, **so that** I get the cheaper option without extra configuration.
  - **Acceptance Criteria:**
    - [ ] With no override, `cpu_architecture` resolves to `"arm64"` and the instance defaults to `t4g.micro`.
    - [ ] On an existing x86_64 deployment, applying this change **replaces** the EC2 instance (new arm64 AMI + instance); the `terraform plan` makes that replacement explicit and it is the only host-level replacement.

### 2.3 Dual-architecture dashboard release (prerequisite)

- **As the operator, I want** the dashboard release to provide both an amd64 and an arm64 binary, **so that** whichever architecture I pick has a verified binary to boot with.
  - **Acceptance Criteria:**
    - [ ] The release pipeline publishes both `wireguard-dashboard-amd64` and `wireguard-dashboard-arm64` assets for a tag.
    - [ ] A single `SHA256SUMS` covers both binaries.
    - [ ] This dual-arch release is cut **before** the architecture default flips to arm64; the spec/PR records that pinning a pre-arm64 release tag with the new boot script is unsupported (no arm64 asset → boot fails fast).

### 2.4 Architecture-agnostic boot

- **As the operator, I want** the instance to fetch the correct binaries automatically for whatever architecture it is, **so that** the boot script doesn't need per-arch configuration.
  - **Acceptance Criteria:**
    - [ ] At boot the instance detects its architecture (`x86_64`/`aarch64`) and selects the matching AWS CLI installer and the matching dashboard binary asset.
    - [ ] The downloaded dashboard binary is checksum-verified against `SHA256SUMS` before install (existing verification behavior preserved).
    - [ ] On `arm64`, the WireGuard service and the dashboard come up and behave identically to `x86_64` (status, throughput, history, geo map, config download, alerting).
    - [ ] A missing/mismatched binary for the running architecture aborts provisioning loudly (no silent half-install), consistent with today's fail-hard boot behavior.

---

## 3. Scope and Boundaries

### In-Scope

- Architecture toggle, arch-derived AMI + default instance type, arm64 default (Terraform `dev/`).
- Dual-arch dashboard binary build + multi-arch `SHA256SUMS` (release pipeline).
- Architecture-agnostic boot (AWS CLI + dashboard binary selection by runtime detection).

### Out-of-Scope

- **Mixed-architecture / multi-instance fleets** — one host, one architecture per deploy.
- **Any VPN/dashboard feature change** — purely an architecture-portability change.
- **CI for infrastructure / automated apply** — apply stays manual and local.
- **Cross-arch validation of every instance family** — the toggle guarantees a correct AMI/instance pairing for the default; a manual mismatched `instance_type` override surfaces at apply (AWS launch error), not via a static family table.
- **All other roadmap items** (specs 001–012, and the remaining open-source-readiness work) are separate and out of scope here.
