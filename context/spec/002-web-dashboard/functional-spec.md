# Functional Specification: Web Dashboard for WireGuard VPN

- **Roadmap Item:** Proposed addition — not currently on the roadmap
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today, the only way to observe the health of the WireGuard VPN is to SSH into the EC2 instance and run `wg show`, `systemctl status wg-quick@wg0`, `top`, and `journalctl -u wg-quick@wg0`. There's no at-a-glance answer to "is the VPN healthy and is everyone connecting?" — and on a phone, away from a laptop, there's effectively no answer at all.

This specification defines a **read-only web dashboard** that surfaces tunnel and server health in a single page. It serves the project's core success metric — _operational reliability_ — by making problems visible immediately and removing the friction of an SSH session.

**Audience for this UI:** the solo operator who deployed the VPN. End users (VPN clients) are not consumers of this dashboard.

**Success looks like:** the operator can answer "is the VPN healthy?" in under 10 seconds, from any device, without an SSH session.

---

## 2. Functional Requirements (The "What")

### 2.1 Client list with connection status

- **As the operator, I want to** see all configured clients with their connection status, **so that I can** spot who's online and who hasn't connected recently.
  - **Acceptance Criteria:**
    - [ ] Each row shows: client name (from `clients_config` in `main.tf`), assigned WireGuard IP, online/offline indicator, last-handshake timestamp ("3 min ago"), and cumulative bytes sent / received per client.
    - [ ] A client with a handshake in the last 3 minutes is shown as **Online** (green); older or never = **Offline** (grey).
    - [ ] When no clients are configured, the list shows the empty-state message: "No clients configured. Add via `terraform/dev/main.tf`."

### 2.2 Server CPU and memory

- **As the operator, I want to** see CPU and memory usage with a 24-hour trend, **so that I can** notice resource pressure before it causes a service issue.
  - **Acceptance Criteria:**
    - [ ] Current CPU % and memory % are shown as large numeric values.
    - [ ] A 24-hour trend line chart is shown for each, at no coarser than 1-minute resolution.
    - [ ] The trend chart re-flows to a full-width single column on screens narrower than ~600px.

### 2.3 WireGuard service uptime

- **As the operator, I want to** see how long the WireGuard service has been continuously running, **so that I can** judge VPN-level stability independent of the host.
  - **Acceptance Criteria:**
    - [ ] "Uptime" displays time since `wg-quick@wg0` last started, in human form (e.g., "3d 14h").
    - [ ] If the service is currently stopped, the field shows **"Service down"** in red instead of a duration.

### 2.4 Network traffic

- **As the operator, I want to** see aggregate VPN traffic over the last 24 hours, **so that I can** monitor bandwidth usage.
  - **Acceptance Criteria:**
    - [ ] Current rx/tx rate (e.g., "1.2 MB/s in / 480 KB/s out") shown as a current value.
    - [ ] 24-hour trend chart for rx and tx on the WireGuard interface, at no coarser than 1-minute resolution.

### 2.5 WireGuard service health detail

- **As the operator, I want to** see WireGuard service health in detail, **so that I can** detect silent service failures the basic uptime number would hide.
  - **Acceptance Criteria:**
    - [ ] Service status indicator: **Running** (green) / **Stopped** (red).
    - [ ] Timestamp of the last service restart.
    - [ ] A list of the most recent handshake events from the last hour, each row showing: client name, event type (handshake), and timestamp.

### 2.6 Server endpoint info

- **As the operator, I want to** see the server's endpoint info on the dashboard, **so that I can** quickly copy values when configuring a new client.
  - **Acceptance Criteria:**
    - [ ] Card displays: server public IP, listening UDP port (51820), and server public key.
    - [ ] The public key has a one-click copy-to-clipboard button.

### 2.7 Authentication

- **As the operator, I want to** authenticate before seeing the dashboard, **so that** the public-facing URL isn't accessible to anyone on the internet.
  - **Acceptance Criteria:**
    - [ ] Visiting any dashboard route triggers a standard HTTP Basic auth challenge.
    - [ ] Credentials are sourced from AWS SSM Parameter Store (the same pattern used for the WireGuard private key) — username and password hash, never plaintext.
    - [ ] Failed authentication returns a 401 challenge response.
    - [ ] [NEEDS CLARIFICATION: should the UI rate-limit or lock out after N failed attempts to limit brute-force exposure on the public endpoint? E.g., 5 failures per minute per IP.]

### 2.8 Auto-refresh

- **As the operator, I want to** see live values without clicking refresh, **so that** the dashboard reflects current state.
  - **Acceptance Criteria:**
    - [ ] The dashboard auto-refreshes its data every 10 seconds.
    - [ ] If a refresh fails, the previously rendered values remain visible and a "Stale data" indicator is shown until the next successful refresh.

### 2.9 Mobile-responsive layout

- **As the operator, I want to** access the dashboard from my phone, **so that I can** check VPN health when I'm away from a laptop.
  - **Acceptance Criteria:**
    - [ ] Layout has no horizontal scrolling at viewport widths ≥ 360px.
    - [ ] Charts and cards re-flow to a single column below ~600px.
    - [ ] Interactive elements (buttons, copy actions) have touch targets ≥ 44px.

### 2.10 HTTPS endpoint at a memorable domain

- **As the operator, I want to** reach the dashboard at a clean HTTPS URL with a valid certificate, **so that I** don't get browser warnings or have to remember a public IP.
  - **Acceptance Criteria:**
    - [ ] Dashboard is reachable at **`https://vk.provectus.pro`**.
    - [ ] A new Route53 hosted zone for `vk.provectus.pro` is delegated from the existing `provectus.pro` zone.
    - [ ] TLS is provided by an AWS ACM certificate (DNS-validated against the new zone).
    - [ ] HTTP (port 80) requests redirect to HTTPS.

---

## 3. Scope and Boundaries

### In-Scope

- Read-only web dashboard with the widgets defined in §2.1–§2.6.
- HTTP Basic authentication, with credentials sourced from SSM Parameter Store.
- HTTPS exposure via ACM certificate at `https://vk.provectus.pro` (new Route53 zone delegated from `provectus.pro`).
- 10-second auto-refresh of dashboard data.
- Mobile-responsive layout.
- Last-24-hours trend storage for CPU, memory, and network traffic.
- Last-hour handshake event log.

### Out-of-Scope

- **Client management actions** — adding, removing, or regenerating clients. Terraform (`clients_config` in `main.tf`) remains the single source of truth.
- **Service control actions** — restarting `wg-quick@wg0`, rebooting the EC2, or any other write/destructive operation.
- **Disk usage widget** (explicitly de-scoped during clarification).
- **Notifications and alerting** — no email, SMS, push, or webhook notifications. The dashboard is pure pull.
- **Multi-user access and role-based authorization** — solo operator only; no per-user views, no admin/end-user split.
- **Alternative auth methods** — no SSO, OAuth, Cloudflare Tunnel, or Tailscale gating in this version.
- **Historical data beyond 24 hours** — no 7-day, 30-day, or configurable-window views.
- **Other roadmap items** are addressed in their own specifications: Project Scaffolding, WireGuard Server Deployment, Multi-Client Support, Quality & Documentation (Pre-Commit Hooks), User Journey Documentation.
