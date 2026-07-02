# Functional Specification: UI-First Client Management

- **Roadmap Item:** Replace spec-018's declarative-`clients_config` + S3-bridge design with a single UI-authoritative model — peers are managed only via the dashboard (and an optional on-box script); Terraform seeds just one admin bootstrap peer; `cloud` keeps S3 as a pure backup.
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 018 kept **two sources of truth** for peers: a declarative `clients_config` list in Terraform (with a warn-only S3 drift `check`) *and* the UI/S3 runtime list. Validated live, that dual authority proved confusing and costly:

- Editing `clients_config` in git **replaces the EC2 instance** — the peer list feeds `user_data`, which rolls a new launch-template version, which is `ForceNew` for `aws_instance`.
- The same edit **registers drift** against the S3 object, *even though the running box reads peers from S3, not `clients_config`*.

So the declarative path both churns the instance and fights the UI-authoritative runtime. This refactor collapses to one obvious model: **the dashboard UI is the only way to manage peers.** Terraform seeds a single **admin bootstrap peer** for anti-lockout and nothing more; `cloud` mode keeps the S3 object purely as a durable **backup** (no Terraform reads it, no drift detection).

**Desired outcome / success:**

- Peer add/edit/remove happens only through the UI (or the equivalent on-box script) — never through Terraform.
- A peer change **never replaces the instance** and **never shows as `terraform plan` drift**.
- A fresh or rebuilt box always comes up with the admin peer connectable (anti-lockout on the AWS path), and in `cloud` mode restores the full UI-managed peer set from S3.
- One clear mental model per deployment: `local` = SQLite only; `cloud` = SQLite + S3 backup.

---

## 2. Functional Requirements (The "What")

### 2.1 Remove the declarative peer list

- **As a maintainer, I want a single peer-management path, so that git and the UI can't disagree.**
  - **Acceptance Criteria:**
    - [ ] `clients_config` (the multi-peer list) is removed from Terraform; there is no Terraform-managed peer set.
    - [ ] The spec-018 S3 drift `check` and the canonical-JSON-vs-`clients_config` comparison are removed.
    - [ ] Normal peer operations (add/edit/remove/enable-disable) never cause an EC2 instance replacement and never appear as `terraform plan` drift.

### 2.2 Admin bootstrap peer (Terraform / AWS path only)

- **As an operator deploying via Terraform, I want one admin peer seeded automatically, so that I can configure WireGuard fast and reach the VPN-only dashboard on a fresh deploy without locking myself out.**
  - **Acceptance Criteria:**
    - [ ] Terraform accepts **one admin peer = name + public key** (the operator does off-host keygen; **no private key** ever enters Terraform, state, SSM, or S3).
    - [ ] On first boot the admin peer is present on the WG server and in the dashboard store, so the operator can connect and open the VPN-only dashboard immediately.
    - [ ] The admin peer is seeded **only when the store has no peers**, so it never resurrects a deliberately-removed or renamed admin; afterward it is an ordinary, UI-editable peer.
    - [ ] The **standalone / manual-VPS path has no Terraform admin seed** — there is no Terraform there. The first peer is added via the script or the UI (identical action, see 2.5).

### 2.3 `local` mode — SQLite only

- **As an operator using local mode, I want peers managed live in the dashboard with no external store.**
  - **Acceptance Criteria:**
    - [ ] Peers live only in on-box SQLite; UI add/edit/remove is applied live via `wg syncconf` (SQLite is the runtime source of truth) — identical to spec 015.
    - [ ] Nothing is written to S3; no AWS store is required. This is the default and the inherent mode of a standalone VPS.

### 2.4 `cloud` mode — SQLite + S3 backup

- **As an operator using cloud mode, I want the UI-managed peer list mirrored to a durable store, so that it survives instance replacement/rebuild — without any Terraform-managed list or drift.**
  - **Acceptance Criteria:**
    - [ ] Every UI (or script) mutation writes SQLite + `wg syncconf` **and** a write-through to the S3 backup object.
    - [ ] On boot the dashboard **restores** peers from the S3 backup into SQLite and applies them, so a replaced/rebuilt instance keeps the UI-managed peers.
    - [ ] If the S3 object is missing/empty on boot, the box initializes it from the admin bootstrap peer (AWS path) or an empty set (VPS) — **never a wipe**; any other S3 error fails safe and does **not** clobber the local list (retains the PR #53 / PR #54 hardening: empty ≠ authoritative, `storeReady` guard, best-effort write-through, self-heal).
    - [ ] **Terraform never reads or reconciles the peer list, and there is no drift detection** — S3 is a pure runtime backup, not a bridge.
    - [ ] S3 access is **least-privilege** (Get/Put on the single object) via the instance role; the list is non-sensitive (names, tunnel IPs, *public* keys) → SSE-S3 is sufficient.

### 2.5 Optional on-box peer-management script

- **As someone installing WireGuard manually on a VPS, I want to add my first peer from the server shell, so that I can get on the VPN and reach the dashboard without SSH-forwarding the dashboard port.**
  - **Acceptance Criteria:**
    - [ ] An on-box CLI can **add / remove / update** a peer, mutating the **same dashboard store the UI uses** (SQLite + `wg syncconf`, + the S3 backup in cloud) — so the script and the UI are equivalent and never diverge.
    - [ ] On **add**, the script **server-generates the keypair by default** (so it can return a usable config), with an optional bring-your-own `--pubkey`.
    - [ ] A **`--show-config`** flag prints the new peer's full WireGuard client config (`[Interface]` + `[Peer]`, including the private key when the keypair was server-generated) to stdout for copy-paste to the client device.
    - [ ] The script is **optional** — not required on the Terraform/AWS path (which uses the admin-peer seed); its purpose is first-peer onboarding on a manual VPS.
    - [ ] Proposed command surface: `wg-peer add <name> [--pubkey KEY] [--show-config]`, `wg-peer remove <name>`, `wg-peer update <name> [--name NEW | --pubkey KEY]`.

---

## 3. Scope and Boundaries

### In-Scope

- Removing `clients_config` (the declarative list) and the spec-018 S3 drift `check`.
- A single admin bootstrap peer (name + public key) seeded by Terraform on the AWS path, only when the store is empty.
- `local` = SQLite-only peers; `cloud` = SQLite + S3 as a durable, UI-driven backup (write-through + boot restore), with no Terraform reads and no drift.
- An optional on-box peer-management script (`add`/`remove`/`update`) with server-side keygen and a `--show-config` flag, equivalent to the UI (it drives the local dashboard API).
- The installer **always deploys the dashboard alongside WireGuard** — the WG-only install path is removed (other tooling covers a bare WG server).
- Removing the spec-015/016 **drift badge and HCL/tfvars export** (their git baseline, `clients_config`, is gone).
- Migration is a **clean re-deploy** (this is a test environment) — no in-place migration tooling.

### Out-of-Scope

- **Public dashboard + bearer token + TLS** — the dashboard stays VPN-only; a future security spec if off-VPN management is ever needed.
- **Multi-admin / RBAC / per-user auth.**
- **Any git-declarative peer set or Terraform-readable S3 "bridge"** — explicitly removed; the UI is the only authority.
- **The spec-018 warn-only drift check and `clients_config` list** — removed, not carried forward.
- **Repository open-sourcing, CI, and git-history purge** — separate roadmap item.
