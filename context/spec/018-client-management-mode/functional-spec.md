# Functional Specification: Client Management Mode (local | cloud)

- **Roadmap Item:** Replace the spec-017 `manage_peers_via_api` bool with a single `client_management_mode` (local | cloud) that picks one clear peer-management path per mode — SQLite-only (local) or an **S3-backed, UI-authoritative bridge** between the dashboard and Terraform (cloud) — eliminating the spec-017 lockout footguns without any instance replacement, and fully removing the live REST-API path.
- **Status:** Completed (2026-07-02 — implemented & owner-validated live as dashboard v0.0.15)
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 017 made Terraform authoritative for peers by driving the dashboard's REST API live over the VPN. It worked, but it introduced two real footguns that bit in practice:

1. **Destroy/flag-off wiped every peer.** The `restapi_object`'s destroy PUT an empty set, so flipping the flag off (or `terraform destroy`) reconciled the server to zero peers — dropping the operator's own tunnel and locking them out of the VPN-only dashboard.
2. **Cold-start on rebuild.** With the flag on, user-data seeded zero peers, so a fresh instance booted with nobody able to connect.

An interim design (this spec's first draft) tried a `declared` mode that auto-**replaced** the EC2 instance on every `clients_config` edit. That removed the lockouts but traded them for full-instance churn and a brief tunnel drop on every peer change — heavier than a small VPN warrants.

**The chosen approach — an S3 bridge.** A single versioned S3 object holds the client list as JSON. It is the durable source of truth that both the dashboard and Terraform can see:

- The **dashboard** reads it at boot and rewrites it on every UI edit — so peer changes apply live (via `wg syncconf`, spec 015), with **no instance replacement and no tunnel drop**, and the list **survives instance replacement/rebuild**.
- **Terraform** seeds the object once from `clients_config` and, on every `plan`/`apply`, **compares `clients_config` against the live object and warns on drift** — without ever reverting the UI. `clients_config` is the declared record; the UI is authoritative.

This keeps peer management simple and live (the operator's day-to-day is the UI), gives git a durable, inspectable record and drift visibility, and makes both spec-017 lockouts structurally impossible (the seed always includes the operator's own peer; nothing ever PUTs an empty set).

**Desired outcome:** one obvious knob — `client_management_mode` — with two coherent modes:

- **`local`** (default): peers live only in the on-box SQLite (today's spec-015 behavior). The standalone VPS is inherently this mode.
- **`cloud`:** peers are mirrored to a versioned S3 object — durable across rebuilds, seeded by Terraform, edited live in the UI, with Terraform surfacing drift between git and the live list.

**Success:**

- A single documented variable selects the whole peer-management behavior; neither mode can reach the spec-017 lockout states.
- In `cloud`, UI peer edits apply live, persist across instance replacement, and are visible to Terraform; `terraform plan` reports when the live list has diverged from `clients_config`, and `apply` never silently reverts the UI.
- In `local`, peer management works exactly as it does today (spec 015), with no external store and no instance churn.

---

## 2. Functional Requirements (The "What")

### 2.1 A single `client_management_mode` variable

- **As an operator, I want one clear mode setting, so that peer-management behavior is obvious and not spread across implicit flags.**
  - **Acceptance Criteria:**
    - [x] A Terraform variable `client_management_mode` accepts exactly `"local"` or `"cloud"`, validated, and **defaults to `"local"`**.
    - [x] It **replaces** the spec-017 `manage_peers_via_api` bool (that variable is removed).
    - [x] The active mode is passed to the dashboard so it knows whether to use the S3 bridge (see 2.3).

### 2.2 `local` mode — SQLite-only (unchanged spec-015 behavior)

- **As an operator using local mode, I want to manage peers live in the dashboard with no external dependencies, so that quick changes need no Terraform and no cloud store.**
  - **Acceptance Criteria:**
    - [x] Peers are added/edited/removed from the dashboard UI, applied live via `wg syncconf` (SQLite is the runtime source of truth) — identical to spec 015.
    - [x] Nothing is written to S3; no AWS store is required.
    - [x] `clients_config` acts only as the **first-boot seed** (via user-data → clients.json, as today); the standalone VPS (no Terraform/AWS) is inherently this mode.
    - [x] This is the default when `client_management_mode` is unset.

### 2.3 `cloud` mode — S3-backed, UI-authoritative bridge

- **As an operator using cloud mode, I want peer edits to apply live and also persist in a durable store that Terraform can see, so that the list survives rebuilds and git stays aware of it — without instance churn or a live API.**
  - **Acceptance Criteria:**
    - [x] The client list is stored as a JSON object in a **versioned S3 bucket** and mirrored in the on-box SQLite.
    - [x] The dashboard **reads the S3 object at boot**, reconciles SQLite, and applies it via `wg syncconf`. A **replaced or rebuilt instance re-reads the current list** → same peers, **no cold-start lockout**, nothing about peers baked into user-data for this mode.
    - [x] On every UI mutation (add/edit/remove/enable-disable), the dashboard applies it live to SQLite + `wg syncconf` **and writes the updated list back to S3**. **No EC2 instance replacement occurs on peer changes.**
    - [x] The **UI is fully functional** in cloud mode (no hidden controls) — it is the primary editor.
    - [x] S3 access is **least-privilege** (`GetObject`/`PutObject` on the single object) using the instance role; the client list is **not sensitive** (names, tunnel IPs, *public* keys), so plain S3 encryption (SSE-S3) is sufficient — no KMS/SecureString.
    - [x] If the S3 object is missing on boot (never seeded / deleted), the dashboard initializes it from the local boot seed; any other S3 read/write error **fails loudly and does not clobber** the list.

### 2.4 Terraform ↔ S3 bridge: seed + drift (warn-only, UI wins)

- **As an operator, I want to seed the list from git and be told when the UI has diverged, without Terraform ever reverting my UI edits.**
  - **Acceptance Criteria:**
    - [x] Terraform **seeds the S3 object once** from `clients_config` on initial setup, then **never overwrites it** (UI-authoritative).
    - [x] On `terraform plan`/`apply`, Terraform **compares `clients_config` against the live S3 object** and emits a **warning when they diverge** (drift detection). It does **not** revert the object — UI edits always win.
    - [x] The operator reconciles a warning by updating `clients_config` to match the live list (documentation-of-record), or by changing the list back in the UI. The escape hatch to force a re-seed is deleting the S3 object (next boot re-seeds from `clients_config`).
    - [x] The drift comparison uses a **canonical JSON shape** (fields `{name, address, public_key}` only, deterministically ordered) produced identically by Terraform and the dashboard, so it never reports **phantom drift** from key ordering or formatting.
    - [x] Per-peer runtime state that is not part of `clients_config` (e.g. `enabled`/disabled) is **intentionally excluded** from the S3 bridge object, so toggling it in the UI does not register as git drift.

### 2.5 Spec-017 live REST-API path removed

- **As a maintainer, I want the superseded live-API machinery gone, so the codebase has one obvious peer-management path per mode.**
  - **Acceptance Criteria:**
    - [x] The `restapi` provider, the `restapi_object`, and the `manage_peers_via_api`/`enable_restapi_peer_sync` flags are **removed** from Terraform.
    - [x] The interim `declared`-mode auto-replace machinery (`terraform_data` peer-replace trigger + `replace_triggered_by`, the cosmetic UI-hide, and the `install.sh` mode gate) is **removed**.
    - [x] The dashboard's `PUT /api/clients` bulk endpoint may remain as a plain runtime endpoint (it is harmless and unused by Terraform), but nothing in Terraform calls it.

---

## 3. Scope and Boundaries

### In-Scope

- A single validated `client_management_mode` (local | cloud, default local) variable replacing `manage_peers_via_api`.
- `local` = spec-015 SQLite-only peers (no external store); `cloud` = a versioned S3 object as a UI-authoritative, durable bridge that survives instance replacement.
- Terraform seeds the S3 object once from `clients_config` and reports drift (warn-only) on plan/apply; the UI writes it live on every edit.
- Removing the spec-017 restapi path and the interim declared-mode auto-replace machinery.

### Out-of-Scope

- **Public dashboard + bearer token + TLS** — not in this spec; a future security spec if off-VPN/CI management is ever needed.
- **Terraform-authoritative reconcile (git wins / revert)** — explicitly rejected in favor of warn-only; the UI is authoritative.
- **`enabled`/disabled and other runtime state in the S3 bridge** — the bridge carries only `clients_config`-equivalent fields.
- **Per-peer S3 objects** — the whole list is one object (atomic write, versioned).
- **A separate admin/bootstrap-peer variable** — unnecessary; the `clients_config` seed (which includes the operator's own peer) covers reconnection after a rebuild.
- **Other roadmap items** — remaining open-source-readiness work (repo visibility, git-history purge, CI + branch protection).
