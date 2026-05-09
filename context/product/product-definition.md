# Product Definition: wireguard-vpn

- **Version:** 1.0
- **Status:** Proposed

---

## 1. The Big Picture (The "Why")

### 1.1. Project Vision & Purpose

Offer a fully codified, version-controlled WireGuard VPN infrastructure that can be audited, reproduced, and extended by the community. One repo, one `terraform apply`, and you have a private VPN server — no manual networking steps, no opaque third-party services.

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
  - **Goal:** Wants a self-hosted VPN to route personal traffic through — full control, no logs, no trust issues.
  - **Frustration:** Commercial VPN services are opaque — can't audit the infrastructure, verify logging policies, or customize the setup. Doesn't want to spend a weekend wiring up iptables rules by hand.

### 1.4. Success Metrics

- **Operational reliability:** The deployed VPN maintains stable connectivity with minimal manual intervention — no unplanned downtime from infrastructure drift or misconfiguration.

---

## 2. The Product Experience (The "What")

### 2.1. Core Features

- **One-command VPN deploy** — Fully codified Terraform modules that provision VPC, subnets, EC2, security groups, IAM, and WireGuard configuration in a single `terraform apply`.
- **Multi-client support** — Support multiple WireGuard clients via a configurable client list in `main.tf`, each with unique public keys and IP assignments.

### 2.2. User Journey

1. **Clone** the repository and set `AWS_PROFILE` to their configured AWS credentials.
2. **Configure** — edit `terraform/dev/locals.tf` to set region, project name, CIDR ranges, and instance AMI. Add client public keys to the `clients_config` list in `main.tf`.
3. **Bootstrap** — run `terraform init && terraform plan -out=tfplan && terraform apply tfplan` in `terraform/dev/backend/` to create the S3 state bucket (one-time step).
4. **Deploy** — run `terraform init && terraform plan -out=tfplan && terraform apply tfplan` in `terraform/dev/` to provision the full VPN infrastructure.
5. **Connect** — configure the local WireGuard client with the server's public IP and the corresponding private key, then `wg-quick up`.

---

## 3. Project Boundaries

### 3.1. What's In-Scope for this Version

- Terraform modules for VPC, subnets, routing, and default security group.
- Terraform module for EC2 instance with WireGuard installed and configured via cloud-init user-data.
- IAM role with SSM access for retrieving the server's WireGuard private key at boot.
- Security group rules for WireGuard (UDP 51820) and SSH access.
- Multi-client configuration via a Terraform variable list.
- Pre-commit hooks for code quality (fmt, tflint, trivy, docs).
- S3 remote state with native locking (no DynamoDB).

### 3.2. What's Out-of-Scope (Non-Goals)

- **CI/CD pipeline** — all plan/apply operations are manual and local. No automated apply workflows.
