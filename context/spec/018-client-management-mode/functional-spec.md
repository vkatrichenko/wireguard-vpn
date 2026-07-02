# Functional Specification: Client Management Mode (local | cloud)

- **Roadmap Item:** Replace the spec-017 `manage_peers_via_api` bool with a single `client_management_mode` (local | cloud) that picks one clear peer-management path per mode — UI-managed (local) or Terraform-via-user-data with instance auto-replace (cloud) — eliminating the spec-017 lockout footguns and demoting the live API to a dormant nice-to-have.
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 017 made Terraform authoritative for peers by driving the dashboard's REST API live over the VPN. It worked, but it introduced two real footguns that bit in practice:

1. **Destroy/flag-off wiped every peer.** The `restapi_object`'s destroy PUT an empty set, so flipping the flag off (or `terraform destroy`) reconciled the server to zero peers — dropping the operator's own tunnel and locking them out of the VPN-only dashboard.
2. **Cold-start on rebuild.** With the flag on, user-data seeded zero peers, so a fresh instance booted with nobody able to connect.

Both stem from the same root cause: a *clever live-API* control plane with sharp edges. The operator's conclusion after the incident: for a small, self-hosted VPN where peer changes are infrequent, **simple and robust beats live and clever.**

**Desired outcome:** one obvious knob — `client_management_mode` — with two coherent modes, each having exactly one management path:

- **`local`:** manage peers by hand in the dashboard UI (today's spec-015 behavior). Nothing in Terraform reaches the running box for peers.
- **`cloud`:** declare peers in Terraform (`clients_config`); an apply re-provisions the box to match. GitOps-by-rebuild — no live API, no VPN-reachability requirement, no empty-PUT wipe, no cold-start lockout.

The spec-017 live-API machinery isn't deleted — it's kept dormant behind an off-by-default advanced flag, available if a future need (e.g. CI/off-VPN management) justifies it.

**Success:**

- A single documented variable selects the whole peer-management behavior; there is no way to get into the spec-017 lockout states through normal use of either mode.
- In `cloud`, editing `clients_config` and running `apply` results in the running server matching the declared set, with the operator still able to connect afterward.
- In `local`, peer management works exactly as it does today (spec 015), with no instance replacement on peer edits.

---

## 2. Functional Requirements (The "What")

### 2.1 A single `client_management_mode` variable

- **As an operator, I want one clear mode setting, so that peer-management behavior is obvious and not spread across implicit flags.**
  - **Acceptance Criteria:**
    - [ ] A Terraform variable `client_management_mode` accepts exactly `"local"` or `"cloud"`, validated, and **defaults to `"local"`**.
    - [ ] It **replaces** the spec-017 `manage_peers_via_api` bool (that variable is removed).
    - [ ] The chosen mode is recorded/derivable so the dashboard can adjust its UI (see 2.4).

### 2.2 `local` mode — UI-managed peers (unchanged spec-015 behavior)

- **As an operator using local mode, I want to manage peers live in the dashboard, so that quick changes need no Terraform and no instance churn.**
  - **Acceptance Criteria:**
    - [ ] Peers are added/edited/removed from the dashboard UI, applied live via `wg syncconf` (SQLite is the runtime source of truth) — identical to spec 015.
    - [ ] UI peer edits **never** trigger an EC2 instance replacement (they don't touch user-data).
    - [ ] `clients_config` acts only as the **first-boot seed**; the standalone VPS (no Terraform) is inherently this mode.
    - [ ] This is the default when `client_management_mode` is unset.

### 2.3 `cloud` mode — Terraform-declared peers via user-data + auto-replace

- **As an operator using cloud mode, I want to declare peers in git and have `apply` make them live, so that the peer set is versioned and reproducible without a live API.**
  - **Acceptance Criteria:**
    - [ ] Peers are declared in `clients_config` and delivered to the box via **user-data** (no REST API in the loop).
    - [ ] Editing `clients_config` and running `terraform apply` **automatically replaces the instance** (create-before-destroy): a new box boots and seeds the updated peer set.
    - [ ] Across the replacement, the **public endpoint (EIP) and the server's identity (SSM-stored private key) persist**, so existing client `.conf` files remain valid without regeneration.
    - [ ] A **brief tunnel drop during the cutover is accepted** and documented (peers reconnect once the new box is up).
    - [ ] Because `clients_config` is the **full** declared set (and includes the operator's own peer), any rebuild re-seeds the operator — there is **no lockout** and **no separate admin/bootstrap-peer variable**.
    - [ ] Changing a *non-peer* input (e.g. dashboard release tag, webhook) does not gain new replacement behavior as a side effect of this change beyond what is intended for cloud peer management (the replace trigger is scoped to the peer set) — _implementation detail resolved in the technical spec._

### 2.4 Dashboard UI reflects the mode

- **As an operator, I want the dashboard to match the active mode, so that I'm not offered controls that don't apply.**
  - **Acceptance Criteria:**
    - [ ] The dashboard learns the active mode (passed in via user-data/config).
    - [ ] In `cloud` mode, the client **add/edit/remove controls are hidden** (cosmetic), with a short note that peers are Terraform-managed.
    - [ ] In `cloud` mode, the **drift badge is hidden** (the box always equals `clients_config`, so divergence is not a meaningful concept).
    - [ ] The hiding is **cosmetic only** — the underlying endpoints are not blocked; a direct API call still works (and is simply pointless, since the next apply re-provisions from `clients_config`).
    - [ ] In `local` mode, the UI is unchanged from today (controls and drift badge shown).

### 2.5 Spec-017 live API demoted to dormant

- **As a maintainer, I want the live-API path kept but out of the way, so that it's available for the future without complicating normal use.**
  - **Acceptance Criteria:**
    - [ ] The spec-017 `PUT /api/clients` endpoint and the `restapi_object` **remain in the codebase** but are **not part of either mode's normal flow**.
    - [ ] The `restapi_object` is gated behind an **off-by-default advanced flag**, independent of `client_management_mode`.
    - [ ] With that flag off (the default), the destructive spec-017 behaviors (empty-PUT-on-destroy, zero-peer seed) **cannot occur**.

---

## 3. Scope and Boundaries

### In-Scope

- A single validated `client_management_mode` (local | cloud, default local) variable replacing `manage_peers_via_api`.
- `local` = spec-015 UI-managed peers (no instance replacement); `cloud` = `clients_config` via user-data with automatic instance replacement on peer changes (EIP + server key preserved).
- Cosmetic, mode-aware dashboard: hide client-edit controls and the drift badge in cloud mode.
- Demoting the spec-017 live API (`PUT /api/clients` + `restapi_object`) to a dormant, off-by-default advanced feature.

### Out-of-Scope

- **Public dashboard + bearer token + TLS** — not in this spec; a future security spec if off-VPN/CI management is ever needed.
- **The live-API workflow as a primary path** — explicitly demoted, not removed.
- **A separate admin/bootstrap-peer variable** — unnecessary; the full `clients_config` seed covers rebuild reconnection.
- **Server-side blocking of client-mutation endpoints in cloud mode** — the UI hide is cosmetic; Terraform re-provisioning is the reconciler.
- **Other roadmap items** — remaining open-source-readiness work (repo visibility, git-history purge, CI + branch protection).
