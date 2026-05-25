# Functional Specification: Runtime Client Reconcile

- **Roadmap Item:** Phase 4 — Runtime Client Reconcile (Archetype 1)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today, adding or removing a WireGuard client on the deployed server requires an EC2 instance replacement. The current Terraform module renders the `clients_config` list into the EC2 instance's user-data, and cloud-init writes the WireGuard peers section + `/etc/wireguard-dashboard/clients.json` exactly once, at first boot. To change the client list, the only path is `terraform apply -replace=aws_instance.wireguard`, which destroys the instance, recreates it, drops every active VPN connection for tens of seconds to a minute, and forces every existing peer to re-handshake.

This specification defines a change to the deployment loop so that **adding or removing a client takes seconds instead of minutes, never interrupts unrelated peers, and never requires an instance replacement.**

The change keeps Terraform as the single source of truth for clients (no UI-driven mutations) and stays on the current EC2/AWS shape. Portability to non-AWS hosts is a separate, later effort — see [`docs/deployment-options.md`](../../../docs/deployment-options.md) §7 steps 2–6 for the broader trajectory.

What changes is the *transport* between Terraform and the running kernel: client data moves from user-data (one-shot at first boot) to AWS SSM Parameter Store (read at runtime), and the dashboard gains a single "Refresh & Apply" control that diffs the desired state against the kernel and reconciles via `wg syncconf` — which is zero-downtime by design for peer-only changes.

**Audience:** the solo operator who manages the VPN. VPN end users (the people whose laptops dial in) are not consumers of the reconcile UI.

**Success looks like:**

- Adding a client = edit HCL → `terraform apply` (~5 s) → click "Apply" in the dashboard (< 1 s reconcile) → new peer is online.
- Existing peers' sessions are not interrupted during peer add/remove.
- The operator can preview the proposed change before applying it.

---

## 2. Functional Requirements (The "What")

### 2.1 Terraform writes the client list to SSM

- **As the operator, I want to** declare clients in Terraform exactly as I do today, **so that I can** keep IaC as the source of truth without learning a new tool.
  - **Acceptance Criteria:**
    - [ ] The HCL shape of `clients_config` in `terraform/dev/main.tf` is unchanged — a list of `{ name, address, public_key }` objects.
    - [ ] After `terraform apply`, an SSM SecureString parameter (default path `/config/wireguard/clients`, overridable via Terraform locals) contains the JSON-encoded client list.
    - [ ] Updating the list with a `terraform apply` updates the SSM parameter and has **no effect on the running VPN** — the EC2 instance is not replaced, the WireGuard service is not restarted, no peer is dropped.
    - [ ] When the EC2 first boots (or is replaced for unrelated reasons), the WireGuard service starts with the peers section sourced from SSM, so a fresh deployment connects clients on the first boot without manual intervention.

### 2.2 Refresh & Apply control in the dashboard

- **As the operator, I want to** trigger reconciliation from the dashboard after a `terraform apply`, **so that** the latest declared state takes effect on the running tunnel without me touching the host.
  - **Acceptance Criteria:**
    - [ ] A single "Refresh" button is visible on the dashboard. The dashboard remains bound to the WG tunnel IP (`172.16.15.1:8080`), so only a connected VPN peer can reach it.
    - [ ] Clicking "Refresh" fetches the current client list from SSM, validates it, and shows a preview of the change. **No kernel changes happen at this step.**
    - [ ] After Refresh, an "Apply" button appears, gated on the preview. Clicking it executes the reconcile.
    - [ ] If validation fails (duplicate names, duplicate IPs, IPs outside the configured WG CIDR, malformed base64 public keys), the dashboard surfaces the specific problem inline and the "Apply" button is not shown.

### 2.3 Preview-before-apply diff view

- **As the operator, I want to** see exactly what will change before applying, **so that I can** catch mistakes — particularly destructive ones — before they hit the running tunnel.
  - **Acceptance Criteria:**
    - [ ] The preview lists separately: peers to be added, peers to be removed, peers whose attributes will change (address or public key), and peers that are unchanged.
    - [ ] Peer-removal lines are visually distinct (warning style) so the operator cannot miss that an active session will be terminated.
    - [ ] When a removed peer has an active session (handshake within the last 3 minutes), the preview annotates that row with "active session — will be dropped."
    - [ ] If the preview shows no changes, the "Apply" button is replaced with a non-actionable "No changes" indicator.

### 2.4 Zero-downtime reconcile for peer changes

- **As the operator, I want to** add or remove a single client without interrupting other clients' sessions, **so that** routine client management doesn't degrade VPN reliability.
  - **Acceptance Criteria:**
    - [ ] When only the peers section changes (add, remove, or modify peers; no change to the `[Interface]` section), reconcile uses `wg syncconf` and the WireGuard service is **not** restarted.
    - [ ] During and after a peers-only reconcile, all peers that were not touched by the change retain their handshake state — no re-handshake event appears in the dashboard's events list for unchanged peers.
    - [ ] Newly added peers can establish their first handshake immediately after Apply — no service restart and no wait window.
    - [ ] Removed peers' sessions are terminated atomically at Apply time.

### 2.5 Detection of interface-level drift

- **As the operator, I want to** be warned when something has tampered with the running `[Interface]` config out-of-band, **so that I can** investigate before assuming the dashboard's view of the world is accurate.
  - **Acceptance Criteria:**
    - [ ] On Refresh, the dashboard compares the `[Interface]` section in `/etc/wireguard/wg0.conf` against the running kernel state (listen port, server public key) and surfaces a warning banner if they disagree.
    - [ ] When drift is detected, the Apply button is **disabled** — peer reconcile cannot proceed until the drift is resolved manually.
    - [ ] The dashboard does not attempt to apply interface-section changes itself. `wg syncconf` is peer-only by design, so this requirement is enforced by construction rather than by additional UI gating.

### 2.6 Atomic reconcile (success or rollback)

- **As the operator, I want to** trust that a failed Apply leaves the system in its prior state, **so that I can** retry without first investigating what got half-applied.
  - **Acceptance Criteria:**
    - [ ] If validation fails, the kernel is not touched and `/etc/wireguard/wg0.conf` is not modified.
    - [ ] If `wg syncconf` fails (non-zero exit), the prior `/etc/wireguard/wg0.conf` remains the on-disk source of truth and the dashboard shows the kernel's error message.
    - [ ] The local cache file `/etc/wireguard-dashboard/clients.yaml` is updated only after `wg syncconf` succeeds.

### 2.7 Reconcile audit log

- **As the operator, I want to** see a history of past reconciles, **so that I can** correlate operational changes with later observations.
  - **Acceptance Criteria:**
    - [ ] Each successful Apply records a row in the dashboard's existing SQLite store with a timestamp, a summary of the diff (+N added, −M removed, ~K updated), and the outcome (success / failed-with-error).
    - [ ] The dashboard surfaces the most recent ~20 reconcile events on a dedicated card or row, with the diff summary and timestamp visible.
    - [ ] No identifying information about the click source is captured (no user identity, no IP). Admin actions only — consistent with the project's "no logs" privacy posture for VPN traffic.

### 2.8 Read-only config template helper

- **As the operator, I want to** download a pre-filled WireGuard config template for any declared peer, **so that I can** hand it to the person whose device will dial in without retyping server values.
  - **Acceptance Criteria:**
    - [ ] Each row in the client list has a "Download config template" action.
    - [ ] The downloaded file is a valid `.conf` skeleton with `[Interface]` containing the peer's declared address and a `<PASTE_PRIVATE_KEY_HERE>` placeholder, and `[Peer]` containing the server's public key, the server's public endpoint, `Endpoint = <server-ip>:51820`, and `AllowedIPs = 0.0.0.0/0, ::/0`.
    - [ ] The dashboard never knows the peer's private key. The operator and the end user are responsible for filling in the placeholder offline.
    - [ ] The downloaded file is named `<peer-name>.conf`.

### 2.9 Read-only access guarantee

- **As the operator, I want to** trust that the dashboard cannot mutate the *declared* client list, **so that** Terraform remains the single source of truth.
  - **Acceptance Criteria:**
    - [ ] The dashboard does not write to the SSM parameter at any point — only `ssm:GetParameter` is granted in its IAM permissions.
    - [ ] No UI surface allows the operator to add, edit, or remove a client by name, address, or public key.
    - [ ] Reconciles read from SSM and write only to `/etc/wireguard/wg0.conf`, `/etc/wireguard-dashboard/clients.yaml`, and the audit log table.

### 2.10 Graceful handling of SSM unavailability

- **As the operator, I want to** see a clear error if SSM can't be reached, **so that I can** distinguish "no changes pending" from "I have no idea what the source says right now."
  - **Acceptance Criteria:**
    - [ ] If `ssm:GetParameter` returns an error (network, IAM, parameter missing), the dashboard surfaces the AWS error message and the "Apply" button is not shown.
    - [ ] The previously cached client list (last successful fetch, persisted to `/etc/wireguard-dashboard/clients.yaml`) remains visible on the dashboard so the operator can still see who's currently configured on the kernel.
    - [ ] Repeated failures do not corrupt the local cache.

---

## 3. Scope and Boundaries

### In-Scope

- Terraform module changes to publish `clients_config` as an SSM SecureString parameter and remove the corresponding peer rendering from cloud-init user-data.
- IAM role extension to grant the EC2 instance `ssm:GetParameter` on the new parameter path.
- Dashboard binary additions: a client source that reads from SSM, a reconcile engine that uses `wg syncconf` for peer-only changes, a "Refresh & Apply" UI with preview-diff view, a typed-confirmation gate for `[Interface]`-drift changes, an audit log of reconcile events, and the read-only config-template download.
- First-boot behavior: WireGuard service comes up with the SSM-sourced peers without manual intervention, preserving today's "deploy and clients connect" experience.

### Out-of-Scope

- **Portable single-binary distro** — `install` / `uninstall` / `update` subcommands, GitHub Releases distribution, bare-VPS / Hetzner support. All of these are deferred to a future spec — see `docs/deployment-options.md` §7 steps 2–6.
- **File-based client source.** The `fileSource` provider in the deployment-options doc is deferred to the same future spec; this spec ships only the SSM source.
- **Server-side key generation** for peers — QR codes, downloadable `.conf` with the private key inlined. Explicitly excluded. The dashboard never sees client private keys.
- **UI-driven client mutations** — Add / Edit / Remove buttons on the dashboard. Terraform remains the single source of truth.
- **Multi-server topology**, ACLs / per-peer routing policy, peer expiry, peer groups. Not in this slice.
- **Authentication and authorization on the dashboard.** Continues to inherit the v3 "VPN membership is the access gate" decision from spec `002-web-dashboard` — no admin password, no per-operator roles. This is acceptable for the solo-operator persona; it is flagged as a follow-up consideration if and when a multi-tenant scenario arises.
- **VPN-traffic logging.** Out-of-scope for this spec. The reconcile audit log is admin-action-only and does not include VPN packet, peer-traffic, or end-user IP information beyond what the existing 24-hour metrics already collect.
- **SSM parameter size relaxation.** The single Advanced SSM parameter accommodates approximately 50 clients comfortably; migration to a parameter hierarchy is deferred until the client count justifies it.
- **Other roadmap items** are addressed in their own specifications: Project Scaffolding (Phase 1), the WireGuard Server Deployment line items (Phase 2), Multi-Client Support (Phase 3), Quality & Documentation (Phase 3), and Web Dashboard v2 admin views (spec `003-dashboard-admin-views`).
