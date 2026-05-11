# Functional Specification: Web Dashboard for WireGuard VPN

- **Roadmap Item:** Proposed addition — not currently on the roadmap
- **Status:** Completed
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
    - [x] Each row shows: client name (from `clients_config` in `main.tf`), assigned WireGuard IP, online/offline indicator, last-handshake timestamp ("3 min ago"), and cumulative bytes sent / received per client.
    - [x] A client with a handshake in the last 3 minutes is shown as **Online** (green); older or never = **Offline** (grey).
    - [x] When no clients are configured, the list shows the empty-state message: "No clients configured. Add via `terraform/dev/main.tf`."

### 2.2 Server CPU and memory

- **As the operator, I want to** see CPU and memory usage with a 24-hour trend, **so that I can** notice resource pressure before it causes a service issue.
  - **Acceptance Criteria:**
    - [x] Current CPU % and memory % are shown as large numeric values.
    - [x] A 24-hour trend line chart is shown for each, at no coarser than 1-minute resolution.
    - [x] The trend chart re-flows to a full-width single column on screens narrower than ~600px.

### 2.3 WireGuard service uptime

- **As the operator, I want to** see how long the WireGuard service has been continuously running, **so that I can** judge VPN-level stability independent of the host.
  - **Acceptance Criteria:**
    - [x] "Uptime" displays time since `wg-quick@wg0` last started, in human form (e.g., "3d 14h").
    - [x] If the service is currently stopped, the field shows **"Service down"** in red instead of a duration.

### 2.4 Network traffic

- **As the operator, I want to** see aggregate VPN traffic over the last 24 hours, **so that I can** monitor bandwidth usage.
  - **Acceptance Criteria:**
    - [x] Current rx/tx rate (e.g., "1.2 MB/s in / 480 KB/s out") shown as a current value.
    - [x] 24-hour trend chart for rx and tx on the WireGuard interface, at no coarser than 1-minute resolution.

### 2.5 WireGuard service health detail

- **As the operator, I want to** see WireGuard service health in detail, **so that I can** detect silent service failures the basic uptime number would hide.
  - **Acceptance Criteria:**
    - [x] Service status indicator: **Running** (green) / **Stopped** (red).
    - [x] Timestamp of the last service restart.
    - [x] A list of the most recent handshake events from the last hour, each row showing: client name, event type (handshake), and timestamp. _(Capped to the 10 newest per Slice 12.5.)_

### 2.6 Server endpoint info

- **As the operator, I want to** see the server's endpoint info on the dashboard, **so that I can** quickly copy values when configuring a new client.
  - **Acceptance Criteria:**
    - [x] Card displays: server public IP, listening UDP port (51820), and server public key.
    - [x] The public key has a one-click copy-to-clipboard button.

### 2.7 Authentication

- **As the operator, I want to** authenticate before seeing the dashboard, **so that** the public-facing URL isn't accessible to anyone on the internet.
  - **Acceptance Criteria:**
    - [x] Visiting any dashboard route triggers a standard HTTP Basic auth challenge. _— de-scoped in v3 pivot (2026-05-04); VPN client gating replaces in-band auth. The dashboard binds to `172.16.15.1:8080` and is unreachable without a WG client whose `AllowedIPs` covers `172.16.15.1`._
    - [x] Credentials are sourced from AWS SSM Parameter Store (the same pattern used for the WireGuard private key) — username and password hash, never plaintext. _— de-scoped in v3 pivot; no in-band credentials exist._
    - [x] Failed authentication returns a 401 challenge response. _— de-scoped in v3 pivot; no auth layer to fail._
    - [x] [NEEDS CLARIFICATION: should the UI rate-limit or lock out after N failed attempts to limit brute-force exposure on the public endpoint? E.g., 5 failures per minute per IP.] _— moot in v3; no public endpoint, no auth, nothing to brute-force._

### 2.8 Auto-refresh

- **As the operator, I want to** see live values without clicking refresh, **so that** the dashboard reflects current state.
  - **Acceptance Criteria:**
    - [x] The dashboard auto-refreshes its data every 10 seconds.
    - [x] If a refresh fails, the previously rendered values remain visible and a "Stale data" indicator is shown until the next successful refresh.

### 2.9 Mobile-responsive layout

- **As the operator, I want to** access the dashboard from my phone, **so that I can** check VPN health when I'm away from a laptop.
  - **Acceptance Criteria:**
    - [x] Layout has no horizontal scrolling at viewport widths ≥ 360px.
    - [x] Charts and cards re-flow to a single column below ~600px.
    - [x] Interactive elements (buttons, copy actions) have touch targets ≥ 44px.

### 2.10 HTTPS endpoint at a memorable domain

- **As the operator, I want to** reach the dashboard at a clean HTTPS URL with a valid certificate, **so that I** don't get browser warnings or have to remember a public IP.
  - **Acceptance Criteria:**
    - [x] Dashboard is reachable at **`https://vk.provectus.pro`**. _— de-scoped in v3 pivot (2026-05-04); dashboard is reachable only at `http://172.16.15.1:8080` over the WireGuard tunnel. No public edge (ALB / ACM / Route53 / WAF) was provisioned._
    - [x] A new Route53 hosted zone for `vk.provectus.pro` is delegated from the existing `provectus.pro` zone. _— de-scoped in v3 pivot; no delegation needed for VPN-only access._
    - [x] TLS is provided by an AWS ACM certificate (DNS-validated against the new zone). _— de-scoped in v3 pivot; the tunnel itself is the encryption layer._
    - [x] HTTP (port 80) requests redirect to HTTPS. _— de-scoped in v3 pivot; no port 80 listener, no HTTPS termination._

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
