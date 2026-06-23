# Product Overview
## Problem Statement
* Observing the health of a self-hosted WireGuard VPN today means SSH-ing into the EC2 instance and running `wg show`, `systemctl status`, `top`, and `journalctl` — there is no at-a-glance answer to "is the VPN healthy and is everyone connecting?", and from a phone, away from a laptop, effectively no answer at all.

## Objectives & Goals
* Let the solo operator answer "is the VPN healthy?" in under 10 seconds, from any device, without an SSH session.
* Improve operational reliability — the project's core success metric — by making tunnel and host problems visible immediately.
* Current focus: improve stability of the existing dashboard binary (see [context-router.md](context-router.md), STABILITY OVER FEATURES rule).

## Value Proposition
* A read-only ops dashboard shipped as a single static Go binary, deployed alongside the WireGuard server, that surfaces client connectivity, host/tunnel metrics, and service health in one auto-refreshing, mobile-responsive page — no SSH, no extra services.

# Target Audience

* Primary user: the solo operator who deployed the VPN (DevOps engineers and privacy-conscious developers running their own self-hosted WireGuard server). VPN end users are not consumers of this dashboard.
* Access context: from any device including mobile, reached only over the WireGuard tunnel at `http://172.16.15.1:8080` (no public edge, no in-band auth). The operator checks it both at a desk and on a phone while away from a laptop.

# Behaviours

* View all configured clients with connection status: name, WireGuard IP, online/offline indicator, last-handshake time, cumulative bytes sent/received, peer endpoint, and offline GeoLite2 geolocation. Online means a handshake within the last 3 minutes.
* Expand a client row to see a per-client 24-hour rx/tx chart and p95 throughput over the selected range; only one row expands at a time.
* See current CPU% and memory% as large numerics plus 24-hour trend charts at no coarser than 1-minute resolution.
* See WireGuard service status (Running/Stopped), last-restart timestamp, and uptime in human form — or "Service down" in red when stopped.
* See aggregate and per-interface network traffic: current rx/tx rate and 24-hour rx/tx trend charts for the wg0 interface.
* See host triage data: disk usage and a running-processes view.
* See recent handshake and service events (capped to the newest 10).
* See server endpoint info — public IP, UDP port 51820, and server public key with one-click copy-to-clipboard — plus an About view with binary build metadata and EC2/OS details.
* Navigate via six tabs (Overview, Clients, System, Network, Events, About); the active tab persists in the URL fragment and switches via htmx fragment swaps without a full page reload.
* Auto-refresh data every 10 seconds; on a failed refresh the last values remain visible behind a "Stale data" indicator until the next success.

# Design & UX
## Design Assets
* [REQUIRES PRODUCT FILLING: no data in the code]

## Responsive & Adaptive Breakpoints
* Mobile-responsive: no horizontal scrolling at viewport widths >= 360px; charts and cards re-flow to a single column below ~600px; below 600px the tab bar becomes a horizontally scrollable row of pills with no horizontal page scroll.

## Accessibility (a11y)
* Interactive elements (buttons, copy actions) have touch targets >= 44px.
* [REQUIRES PRODUCT FILLING: no formal WCAG / keyboard / screen-reader standard defined in the code]

## Animations & Micro-interactions
* Stale-data indicator shown on the cards when a 10-second refresh fails, until the next successful refresh.
* Inline expand/collapse of a client row to reveal its per-client chart panel (one open at a time).
* Light/dark theming via the bundled theme toggle.

## Technical Details

All techical details are stored in context-router.md

# Non-Functional Requirements
## Browser Support Matrix
* [REQUIRES PRODUCT FILLING: no explicit browser support matrix defined in the code]

## Performance Metrics
* Dashboard handlers return in single-digit milliseconds; HTTP graceful-shutdown drains in-flight requests within a 5-second window.
* Trend storage retains the last 24 hours only, at no coarser than 1-minute resolution (extended time ranges are a draft in spec 003).
* [REQUIRES PRODUCT FILLING: no Core Web Vitals / bundle-size targets defined in the code]

## SEO (Search Engine Optimization)
* Not applicable — the dashboard is reachable only over the WireGuard tunnel and is not a public-facing, indexable site.

# Analytics & Tracking
## Event Tracking Plan
* [REQUIRES PRODUCT FILLING: no analytics or event tracking implemented — the dashboard is pure pull with no telemetry]

# Out of Scope
* Client management actions (add/remove/regenerate clients) — Terraform `clients_config` in `main.tf` stays the single source of truth; the only sanctioned write path is the draft spec-004 "Refresh & Apply" reconcile.
* Service control or destructive actions (restart `wg-quick@wg0`, reboot EC2, any write operation).
* Notifications and alerting (no email, SMS, push, or webhook); multi-user access and role-based authorization; alternative auth methods (SSO, OAuth, Cloudflare Tunnel, Tailscale); a public HTTPS edge (ALB/ACM/Route53/WAF) — all de-scoped in the 2026-05-04 v3 pivot.
* Historical data beyond the supported trend window in the shipped version.

# Milestones & Timeline
* Phase 1 — Network Foundation: VPC, subnets, locked-down default security group, S3 remote state with native locking (delivered).
* Phase 2 — WireGuard Server Deployment: EC2 + cloud-init, IAM/SSM key retrieval, security group rules, end-to-end single-client tunnel.
* Phase 3 — Multi-Client Support and Quality & Documentation (configurable client list, per-client IPs, pre-commit hooks, user-journey docs).
* Phase 4 — Runtime Client Reconcile: SSM-driven client transport, dashboard "Refresh & Apply" via `wg syncconf`, read-only per-peer config-template helper (draft).
* Web Dashboard (spec 002, shipped v3) and Dashboard v2 admin views / six-tab layout / longer time range (spec 003, draft) — extend the operator-facing dashboard beyond the original roadmap.

# Rules for this file (PRD.md)

- Keep this file clear and concise. 1 General change must be described in 1-2 sentences maximum. If more details are needed - update documentation in project-context/ and link it here.
