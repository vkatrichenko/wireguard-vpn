# Functional Specification: Runtime Client Management

- **Roadmap Item:** Manage WireGuard clients (add / remove / edit) at runtime from the dashboard, without a Terraform apply or instance replacement.
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today, changing the set of VPN clients is a code-change-and-redeploy chore. To add one person an operator must: generate a keypair off-host, hand-edit the `clients_config` list in `terraform/dev/main.tf`, and run `terraform apply` — which **replaces the EC2 instance**, causing downtime and dropping *every* active tunnel for a single-peer change. On a standalone VPS install there is no first-class way to manage clients at all.

This feature lets the operator **add, remove, and edit clients live from the dashboard**, applied instantly with no downtime and no instance replacement, working identically on AWS/EC2 and a standalone VPS. Client data is stored on the server so it survives dashboard restarts and reboots. Terraform's `clients_config` becomes a **first-boot seed** rather than the day-to-day editing surface, and the dashboard provides an **export** of the current client list plus a **drift indicator** so the operator can keep the Terraform definition reconciled in git when they choose to.

**Desired outcome:** managing who can connect takes seconds in the dashboard, never disrupts other users, and never requires editing Terraform or rebuilding the server.

**Success:** an operator can add a new client and have it connect, or remove one and have it cut off, entirely from the dashboard, with all other tunnels unaffected, and the change still present after a reboot.

---

## 2. Functional Requirements (The "What")

- **As an operator, I want to add a client from the dashboard by pasting its public key, so that I can grant access without editing Terraform or rebuilding the server.**
  - **Acceptance Criteria:**
    - [ ] The dashboard provides a form to add a client with a name and a WireGuard public key; the client's tunnel IP is auto-assigned to the next free `/32` in the server subnet (with an option to set it explicitly).
    - [ ] The form rejects invalid input: a malformed public key (not 44-char base64), a malformed/`/32` address, a duplicate name, or a duplicate IP — each with a clear message; no partial change is applied on rejection.
    - [ ] On success the new client appears in the client list immediately and its peer is active on the server **without restarting the tunnel** — existing clients' connections are not interrupted.
    - [ ] The existing per-client config download (full / split tunnel) works for the newly added client.
    - [ ] No client private key is ever entered, generated, or stored on the server.

- **As an operator, I want to remove a client from the dashboard, so that I can revoke access instantly.**
  - **Acceptance Criteria:**
    - [ ] Removing a client takes effect immediately — that peer can no longer connect, and an active session for it is dropped.
    - [ ] All other clients' connections remain unaffected.

- **As an operator, I want to edit an existing client, so that I can correct details or temporarily disable access.**
  - **Acceptance Criteria:**
    - [ ] The operator can rename a client, edit a free-text note, change its public key or IP (with the same validation as add), and enable/disable it.
    - [ ] A disabled client cannot connect but is retained in the list and can be re-enabled later.
    - [ ] Edits apply live, without restarting the tunnel or disrupting other clients.

- **As an operator, I want client changes to persist, so that I don't lose them.**
  - **Acceptance Criteria:**
    - [ ] Client changes survive a dashboard process restart and a server reboot (same instance).
    - [ ] On a brand-new server (fresh VPS install, or an EC2 instance that was replaced), the client list is seeded from Terraform's `clients_config`.

- **As an operator, I want to keep my Terraform definition reconciled, so that a rebuild doesn't silently lose clients I added live.**
  - **Acceptance Criteria:**
    - [ ] The dashboard offers an **export** of the current clients as a paste-ready `clients_config` block (and/or `clients.auto.tfvars.json`) for committing to git.
    - [ ] The dashboard shows a **drift indicator** when clients exist on the server that are not present in the Terraform seed it booted with (e.g. "N clients not in your Terraform seed").

- **As an operator, I want this to work the same on a standalone VPS, so that I'm not locked into AWS.**
  - **Acceptance Criteria:**
    - [ ] All add/remove/edit/persist/export/drift behavior works identically on a standalone `install.sh` VPS deployment, with no AWS-specific dependency.

---

## 3. Scope and Boundaries

### In-Scope

- Dashboard UI + endpoints to add, remove, and edit clients (paste-public-key model).
- Auto IP allocation within the server subnet, with manual override and full validation.
- Live application of changes to the running server with no downtime and no instance replacement.
- On-server persistence of the client list (survives restart/reboot).
- First-boot seeding of the client list from Terraform's `clients_config`.
- Export of the current client list as a Terraform-ready block, plus a drift indicator vs. the Terraform seed.
- Identical behavior on EC2 and standalone VPS installs.

### Out-of-Scope

- **Server-side key generation / QR codes** — clients are added by pasting a public key; the server never handles private keys (may be revisited later).
- **Authentication on the management actions** — the dashboard remains VPN-gated and unauthenticated for now; a token/login guard on write actions is future work.
- **Off-box durable storage (S3, external database)** — runtime-added clients live only on the server. A full EC2 *rebuild* re-seeds from Terraform, so clients added live but not yet exported to git are lost on rebuild; this is mitigated by the export + drift indicator, not by remote storage.
- **Terraform as a live writer** — `terraform apply` does not push client changes into a running server; Terraform only seeds first boot.
- Other roadmap items (ARM/Graviton option; remaining open-source readiness work) — separate specifications.
