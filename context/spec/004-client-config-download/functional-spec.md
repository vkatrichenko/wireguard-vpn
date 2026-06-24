# Functional Specification: Client Config Download

- **Roadmap Item:** Not yet on the roadmap (follows on from 003-dashboard-admin-views; first step of the "self-service onboarding" direction)
- **Status:** Completed
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Adding a new WireGuard client today is entirely hand-rolled. After generating a keypair off-host (`wg genkey | wg pubkey`) and adding the public key to `terraform/dev/main.tf`, the operator still has to **assemble the client's `.conf` file by hand** — looking up the server's public key, the Elastic IP endpoint, the client's assigned tunnel address, the right `AllowedIPs`, and a working `DNS` line. Every field is a chance to fumble: a wrong endpoint port, a stale server key after rotation, or `AllowedIPs` that silently route nothing.

The dashboard already knows almost everything needed to write that file: it reads the client roster from `clients.json`, the server's public key via `sudo wg show wg0 public-key`, and the public endpoint via IMDSv2. The only thing it must **not** know is the client's private key — that stays off-host by design (and that constraint is non-negotiable; see [003-dashboard-admin-views](../003-dashboard-admin-views/functional-spec.md), which keeps the dashboard strictly read-only).

This spec lets the operator **download a ready-to-use client config** from the Clients tab, with every server-derived field filled in correctly and only the private key left as a placeholder for the operator to paste locally. It turns a multi-step, error-prone copy job into one click plus one paste.

**Success looks like:** for any configured client, the operator downloads a `.conf`, replaces a single `PrivateKey` line with the key they already hold, and the tunnel connects on first try — in either full-tunnel (exit node) or split-tunnel (private-resources) mode, without SSH and without looking up a single server value by hand.

---

## 2. Functional Requirements (The "What")

### 2.1 Per-client download control

- **As the operator, I want to** download a client's WireGuard config from the dashboard, **so that I can** onboard a device without hand-assembling the file.
  - **Acceptance Criteria:**
    - [x] Each client row in the **Clients** tab exposes a **Download config** control.
    - [x] The control offers a **routing-mode choice** with two options: **Full tunnel** and **Split tunnel** (default selection: **Full tunnel**).
    - [x] Activating the control downloads a file named `wg-<client-name>.conf` (e.g. `wg-vkatrychenko.conf`).
    - [x] The downloaded file is plain text in valid `wg-quick` `.conf` format.
    - [x] The control is available for every client present in `clients.json`; the action requires no SSH and no `terraform` command.

### 2.2 Generated config contents

- **As the operator, I want** every server-derived field pre-filled correctly, **so that I** never have to look up the endpoint, server key, or address by hand.
  - **Acceptance Criteria:**
    - [x] `[Interface] Address` equals the client's assigned tunnel address from `clients.json` (e.g. `172.16.15.6/32`).
    - [x] `[Interface] DNS` equals the **VPC DNS resolver, derived at runtime** from the VPC's primary CIDR (network base + 2) read via IMDSv2 — currently `10.23.0.2` for the `10.23.0.0/16` VPC. The resolver is **not hardcoded**, so it stays correct if the VPC CIDR changes.
    - [x] `[Interface] PrivateKey` is a **clearly-marked placeholder**, not a real key — exact text: `PrivateKey = <paste your client private key here>`.
    - [x] `[Peer] PublicKey` equals the live server public key (from `wg show wg0 public-key`), so the file is correct even after a server-key rotation.
    - [x] `[Peer] Endpoint` equals the server's public endpoint (Elastic IP from instance metadata) and UDP port `51820`, in `host:port` form.
    - [x] `[Peer] PersistentKeepalive` is `25`.
    - [x] No real secret value (no private key) appears anywhere in the file.

### 2.3 Routing modes

- **As the operator, I want to** pick exit-node vs. private-resources routing at download time, **so that** the same dashboard serves both VPN use cases.
  - **Acceptance Criteria:**
    - [x] **Full tunnel** sets `AllowedIPs = 0.0.0.0/0, ::/0` — all client traffic (and DNS) routes through the VPN, using the server's confirmed NAT/masquerade path.
    - [x] **Split tunnel** sets `AllowedIPs` to the WireGuard subnet plus the **VPC's primary CIDR read at runtime** — currently `172.16.15.0/24, 10.23.0.0/16`. Only WireGuard peers and the AWS VPC are routed; the client's local internet is untouched. The VPC CIDR being in scope is what lets the derived DNS resolver resolve in this mode.
    - [x] Switching the routing-mode choice changes **only** the `AllowedIPs` line between the two downloads; every other field is identical.

### 2.4 Operator guidance

- **As the operator, I want** a reminder of the one manual step, **so that I** don't ship a config with the placeholder still in it.
  - **Acceptance Criteria:**
    - [x] The Clients tab shows a short inline hint near the download control, to the effect of: "Replace `PrivateKey` with this client's private key before use — the server never holds it."
    - [x] The hint is visible without expanding any row and does not depend on JavaScript to be readable.

### 2.5 Empty / unresolved states

- **As the operator, I want** sensible behavior when data is missing, **so that** a half-provisioned host doesn't produce a misleading file.
  - **Acceptance Criteria:**
    - [x] If no clients are configured, the Clients tab shows the existing empty state and no download control appears (consistent with 003 §2.3).
    - [x] If the server public key or public endpoint cannot be determined at request time, the download fails with a clear, human-readable error rather than emitting a config with a blank or wrong field.
    - [x] A request for a client name that is not in `clients.json` returns a not-found response, not an empty file.

### 2.6 Access model (unchanged)

- **As the operator, I want** the feature to honor the existing security posture, **so that** nothing new is exposed.
  - **Acceptance Criteria:**
    - [x] The download is reachable only over the WireGuard tunnel at `http://172.16.15.1:8080`, like the rest of the dashboard — no public exposure, no new listener.
    - [x] The feature adds no in-band authentication and removes none; it is gated only by VPN access (consistent with 002/003).
    - [x] The generated file contains only values that are already non-secret (server public key, public endpoint, the client's own assigned address).

---

## 3. Scope and Boundaries

### In-Scope

- A per-client **Download config** control in the Clients tab with a **Full / Split** routing-mode choice.
- Server-side generation of a valid `wg-quick` `.conf` with every server-derived field filled in and a `PrivateKey` placeholder.
- `DNS = 10.23.0.2` (VPC resolver) and the two `AllowedIPs` profiles described above.
- An inline hint telling the operator to paste the private key before use.
- Error/empty handling for missing clients, missing server key, and missing endpoint.

### Out-of-Scope

- **Key generation or storage** — the dashboard never creates, holds, or transmits a client private key. Keypairs stay off-host (`wg genkey`), unchanged from today.
- **In-browser QR code / key-paste assembly** — considered and deliberately deferred; the download ships with a placeholder the operator fills in by hand.
- **Adding / removing / editing clients from the UI** — the client roster is still owned by `terraform/dev/main.tf` → `clients.json`. No write path, no `wg set`, no `terraform apply` from the dashboard (unchanged from 002/003).
- **Server-key rotation** — out of band; this feature only *reads* whatever key is live.
- **Custom / external DNS override (e.g. a public resolver)** — the DNS line is always the VPC resolver derived from the VPC CIDR; no operator-chosen alternate resolver in v1.
- **In-band authentication or HTTPS** — still VPN-gated `http://172.16.15.1:8080`.
- **Binary distribution changes** (GitHub Releases) and **alerts / connection-history / geo-map** — separate specifications.
