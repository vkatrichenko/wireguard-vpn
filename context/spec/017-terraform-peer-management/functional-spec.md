# Functional Specification: Terraform-Managed Peers via REST API

- **Roadmap Item:** Manage the WireGuard peer set from git/Terraform without EC2 replacement — Terraform becomes authoritative for peers by driving the dashboard's REST API (via the `restapi` provider), so manual UI changes surface as `terraform plan` drift and `apply` reconciles the server to match git.
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today the WireGuard peer set has two disconnected control paths that pull in opposite directions:

1. **Terraform `clients_config`** — since spec 015, this is only a *first-boot seed*. After boot it does nothing; the on-box SQLite is the runtime source of truth. So an operator who wants git-tracked, reviewable, versioned peer management has no live mechanism — editing `clients_config` after first boot has no effect.
2. **The dashboard UI** — peers can be added/edited/removed live (spec 015), but those changes live only on the box. There's no git record and no way for Terraform to *know* the live set diverged from what's declared, beyond an in-UI badge.

The result: an operator who prefers a GitOps workflow (declare peers in git, review in a PR, apply) can't have it, and can't tell from Terraform whether someone changed peers by hand.

**Desired outcome:** git/Terraform becomes the authoritative declaration of the peer set. Adding, editing, or removing a peer is a `terraform apply` that reaches the running dashboard's REST API and reconciles the live set — **with no EC2 replacement and no user-data re-run** (it reuses the live `wg syncconf` apply from spec 015). Any change made by hand in the UI shows up as **drift in `terraform plan`**, and `apply` reconciles the server back to git. The UI stays usable for quick changes, but git wins on the next apply.

**Success:**

- Declaring a new peer in git and running `apply` makes that peer live on the server within one apply, with no instance replacement and no tunnel drop for existing peers.
- Editing a peer in the UI, then running `plan`, shows that change as drift on a single Terraform resource; `apply` reverts it to the git-declared value.
- A peer created only in the UI (never in git) also shows as drift and is removed on `apply`.
- The in-UI drift badge and the Terraform view agree on what "diverged" means.

---

## 2. Functional Requirements (The "What")

### 2.1 Terraform is authoritative for the peer set

- **As an operator, I want to declare my WireGuard peers in git and apply them, so that the peer set is versioned, reviewable, and reproducible.**
  - **Acceptance Criteria:**
    - [ ] The existing `clients_config` list in Terraform is the single declared source of truth for peers; the same list continues to seed a fresh box on first boot **and** drives ongoing reconciliation.
    - [ ] Each peer is described by exactly the fields available today — `name`, `address`, `public_key` — with no new required per-peer fields.
    - [ ] Running `terraform apply` reconciles the live peer set on the running server to exactly match `clients_config`: peers present in git but not live are added; peers live but not in git are removed; peers whose declared values differ are updated.
    - [ ] Adding or removing a peer this way causes **no EC2 instance replacement and no user-data re-run** — the change is applied to the running instance.
    - [ ] Existing, unchanged peers keep their tunnels during an apply (no full-interface restart; reuses the `wg syncconf` no-drop apply).

### 2.2 Manual UI changes surface as Terraform drift

- **As an operator, I want any hand-made UI change to show up in `terraform plan`, so that I always know whether the live server has diverged from git.**
  - **Acceptance Criteria:**
    - [ ] The whole peer set is modeled as a **single** Terraform resource, so both an *edit to a declared peer* and a *brand-new UI-only peer* appear as drift on that one resource in `plan`.
    - [ ] `terraform plan` on an unchanged server (live set matches git) shows **no** changes — no phantom/perpetual drift.
    - [ ] When the live set differs from git, `plan` shows a clear diff of what would change, and `apply` reconciles the server to git.
    - [ ] Reconciliation direction is always git → server (git wins); the operator's choice to keep a UI change means promoting it into `clients_config`, not Terraform adopting it silently.

### 2.3 New dashboard bulk endpoint `PUT /api/clients`

- **As the Terraform integration, I need one idempotent endpoint that replaces the whole peer set, so that reconciliation is a single, predictable operation.**
  - **Acceptance Criteria:**
    - [ ] A new `PUT /api/clients` accepts the full desired peer set as its body, in the same `{ "clients_config": [ { name, address, public_key }, … ] }` shape the dashboard already emits from its tfvars export.
    - [ ] The endpoint is **idempotent**: applying the same body twice produces the same live state and the second call is a no-op in effect.
    - [ ] On success it persists the set to SQLite (the runtime source of truth) and applies it live via `wg syncconf` — no tunnel drop for unchanged peers, and the server's own key/identity is never touched.
    - [ ] The request body is validated with the same rules as today's single-peer add/edit (duplicate names/keys/addresses rejected, addresses within the server subnet); on any validation failure **no partial change is applied** and a clear error is returned.
    - [ ] An **empty** declared set is honored — it reconciles the server to zero peers (a full removal). This is a valid, plannable operation, not an error. _(Plan shows the full removal before it happens.)_
    - [ ] Removing a peer that is **currently connected** is allowed (its tunnel drops); the server logs a warning naming each removed-while-connected peer.

### 2.4 Canonical read shape (no phantom drift)

- **As the Terraform integration, I need the peer set read back in a stable, canonical form, so that Terraform doesn't report drift when nothing actually changed.**
  - **Acceptance Criteria:**
    - [ ] The set returned by the dashboard for Terraform to read is **deterministically ordered** (stable sort, e.g. by name) and field values are normalized to match what `PUT` stores, reusing the existing export ordering logic.
    - [ ] Immediately after a successful `apply`, a subsequent `plan` shows no changes.
    - [ ] Fields the integration does not manage (e.g. live status, last-handshake, transfer counters) are **not** part of the managed/compared shape, so runtime activity never registers as drift.

### 2.5 Repointed in-UI drift badge

- **As an operator, I want the dashboard's drift badge to reflect divergence from the git-managed set, so that the in-UI hint and Terraform agree.**
  - **Acceptance Criteria:**
    - [ ] The existing Clients-tab drift badge (from spec 015) is repointed/relabeled to mean "diverged from the git-declared (Terraform-managed) set" rather than "diverged from the first-boot seed."
    - [ ] When the live set matches the last git-applied set, the badge shows no drift; after a hand-made UI change it shows drift — consistent with what `terraform plan` would report.
    - [ ] The badge remains informational only; it does not itself reconcile anything.

### 2.6 Provider wiring and reachability

- **As an operator, I want the Terraform wiring to follow this repo's conventions and the VPN-only posture, so that it's consistent and safe by default.**
  - **Acceptance Criteria:**
    - [ ] The `Mastercard/restapi` provider is pinned **exact** (`= 3.0.0`) per repo convention, with its community-provider status noted in the eventual technical spec's architecture decisions.
    - [ ] The provider targets the dashboard over the **VPN** (the peer's tunnel address); the operator runs `plan`/`apply` while connected to the VPN.
    - [ ] When the dashboard is unreachable (operator not on the VPN), `plan`/`apply` fails with a clear connection error rather than silently reporting a wrong/empty set.
    - [ ] No authentication is added in this spec; the write endpoints stay gated only by VPN reachability, consistent with the current posture.

---

## 3. Scope and Boundaries

### In-Scope

- Making `clients_config` the authoritative, continuously-reconciled peer declaration (both first-boot seed and live reconciliation from one list).
- A new idempotent `PUT /api/clients` bulk-replace endpoint (SQLite + `wg syncconf`, full validation, empty-set = full removal, connected-peer removal allowed + logged).
- A canonical, deterministically-ordered read shape so Terraform sees no phantom drift.
- Modeling the whole peer set as a single `restapi` resource driven by `clients_config`, provider pinned `= 3.0.0`.
- Repointing the spec-015 in-UI drift badge to mean "diverged from the git-managed set."

### Out-of-Scope

- **Authentication / tokens on write endpoints** — deferred to a separate future security spec that would cover the *entire* write surface (UI + API), not just the Terraform path. This spec assumes VPN-only. (Breaks if the dashboard is ever exposed beyond the VPN.)
- **Managing other dashboard config via API** (alert webhook, alert thresholds, etc.) — the provider makes this a trivial future extension, but it is not built here.
- **Standalone-VPS Terraform** — there is no AWS/Terraform on the plain-VPS path; that path stays UI/CLI-managed.
- **Per-peer Terraform resources** — explicitly rejected in favor of the single whole-list resource (needed to surface UI-only peers as drift).
- **Changing the peer data model** — no new peer fields, no server-side key generation, no QR codes.
- **Other roadmap items** — remaining open-source-readiness work (repo visibility flip, git-history blob purge, CI + branch protection) and any Phase 7 (spec 012) E2E follow-ups.
