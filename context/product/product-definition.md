# Product Definition: wireguard-vpn

- **Version:** 1.1
- **Status:** Active

---

## 1. The Big Picture (The "Why")

### 1.1. Project Vision & Purpose

Offer a fully codified, version-controlled WireGuard VPN infrastructure that can be audited, reproduced, and extended by the community. One repo, one `terraform apply`, and you have a private VPN server — no manual networking steps, no opaque third-party services. A bundled, VPN-only web dashboard then lets the operator **see** the server's health and **be told** when something breaks — without SSH.

### 1.2. Target Audience

- **DevOps engineers** who manage cloud infrastructure and want a ready-made, best-practice Terraform module for deploying WireGuard on AWS.
- **Privacy-conscious developers** who want their own VPN for privacy and security but don't want to configure networking from scratch.

### 1.3. User Personas

- **Persona 1: "Alex the Platform Engineer"**
  - **Role:** Senior DevOps engineer at a mid-size company.
  - **Goal:** Needs a clean, auditable Terraform reference for standing up a WireGuard server that the team can review and trust.
  - **Frustration:** Existing open-source WireGuard IaC examples are incomplete, use bad practices, or aren't production-ready. Manual setup across VPC, security groups, IAM, and user-data is error-prone and hard to reproduce.

- **Persona 2: "Jordan the Privacy-First Developer"**
  - **Role:** Freelance backend developer who works from cafes and co-working spaces.
  - **Goal:** Wants a self-hosted VPN to route personal traffic through — full control, no logs, no trust issues. Also wants a quick, glanceable view of who's connected and to be pinged in chat if the tunnel drops.
  - **Frustration:** Commercial VPN services are opaque — can't audit the infrastructure, verify logging policies, or customize the setup. Doesn't want to spend a weekend wiring up iptables rules by hand, or discover hours later that the server was down.

### 1.4. Success Metrics

- **Operational reliability:** The deployed VPN maintains stable connectivity with minimal manual intervention — no unplanned downtime from infrastructure drift or misconfiguration.
- **Operational visibility:** The operator can answer "is it up, who's connected, where from, and is anything wrong?" from the dashboard alone, and is proactively notified (in chat) of failures within a poll interval — without SSH.

---

## 2. The Product Experience (The "What")

### 2.1. Core Features

- **One-command VPN deploy** — Fully codified Terraform modules that provision VPC, subnets, EC2, security groups, IAM, and WireGuard configuration in a single `terraform apply`.
- **Multi-client support** — Support multiple WireGuard clients via a configurable client list in `main.tf`, each with unique public keys and IP assignments.
- **VPN-only observability dashboard** — A self-contained Go web dashboard (reachable only over the tunnel) showing server health, per-client online/offline status, throughput, connection history, and an offline world map of peer locations. Clients can download their own ready-to-use configs (full / split tunnel) from it.
- **Proactive alerting** — The dashboard watches for the failures that matter (service down, high disk, sustained high CPU, a peer over a transfer cap) and fans out notifications to any combination of a Slack-compatible incoming webhook, a Slack bot, Telegram, and Discord — with edge-triggering, cooldown, and recovery messages. The incoming webhook can be managed (set / test / revert) at runtime; the additional transports are opt-in at boot. A Prometheus `GET /metrics` endpoint exposes current VPN health for external scraping (Grafana etc.).
- **Offline & self-contained** — The dashboard makes no outbound requests for its own operation (embedded map + geolocation, no CDNs); it holds no client private keys; the only egress it adds is the opt-in alert transports (and the `/metrics` endpoint is pull-only, making no outbound requests).

### 2.2. User Journey

1. **Clone** the repository and set `AWS_PROFILE` to their configured AWS credentials.
2. **Configure** — edit `terraform/dev/locals.tf` to set region, project name, CIDR ranges, and instance AMI. Add client public keys to the `clients_config` list in `main.tf`. Pin the dashboard release via `dashboard_release_tag`.
3. **Bootstrap** — run `terraform init && terraform plan -out=tfplan && terraform apply tfplan` in `terraform/dev/backend/` to create the S3 state bucket (one-time step).
4. **Deploy** — run `terraform init && terraform plan -out=tfplan && terraform apply tfplan` in `terraform/dev/` to provision the full VPN infrastructure (and, when pinned, the dashboard).
5. **Connect** — configure the local WireGuard client with the server's public IP and the corresponding private key, then `wg-quick up`.
6. **Monitor & be alerted** — over the tunnel, open the dashboard at `http://172.16.15.1:8080` to watch status/throughput/history and the peer map; optionally fan out alerting to a Slack webhook/bot, Telegram, and/or Discord so failures arrive in chat, and scrape the Prometheus `/metrics` endpoint into an external monitoring stack.

---

## 3. Project Boundaries

### 3.1. What's In-Scope for this Version

- Terraform modules for VPC, subnets, routing, and default security group.
- Terraform module for EC2 instance with WireGuard installed and configured via cloud-init user-data.
- IAM role with SSM access for retrieving the server's WireGuard private key at boot.
- Security group rules for WireGuard (UDP 51820) and SSM-based instance access (no SSH port).
- Multi-client configuration via a Terraform variable list.
- Pre-commit hooks for code quality (fmt, tflint, trivy, docs).
- S3 remote state with native locking (no DynamoDB).
- A VPN-only, single-binary web dashboard: server/peer status, throughput, connection history, offline geo map, and per-client config download (specs 002–006).
- Proactive alerting fanned out to a Slack incoming webhook (runtime-managed) plus opt-in Slack bot / Telegram / Discord transports, and a Prometheus `/metrics` endpoint for external scraping (specs 007–008 / 012).

### 3.2. What's Out-of-Scope (Non-Goals)

- **CI/CD pipeline for infrastructure** — all plan/apply operations are manual and local. No automated apply workflows.
- **Dashboard authentication** — access is gated solely by the WireGuard tunnel; there is no login/session layer (revisit only if it becomes multi-user).
- **Email / SMS / PagerDuty alerting** — alerting fans out to chat transports only (Slack incoming webhook, Slack bot, Telegram, Discord); no email/SMTP/SES, SMS, or PagerDuty routing.
- **Authentication or historical data on `/metrics`** — the Prometheus endpoint is VPN-gated (no auth) and exposes only current in-memory values; long-term retention is the scraper's job, not the dashboard's.
- **Persisting runtime dashboard config** — runtime webhook overrides are in-memory and reset to the deploy-time (SSM/env) value on restart; the new boot-config transports are immutable for the process lifetime (no runtime management UI).
