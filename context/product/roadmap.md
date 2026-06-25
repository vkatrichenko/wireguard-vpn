# Product Roadmap: wireguard-vpn

_This roadmap outlines our strategic direction based on user needs and project goals. It focuses on the "what" and "why," not the technical "how."_

_Legend: `[x]` shipped & verified · `[~]` code-complete, pending deploy/E2E · `[ ]` planned/specified._

---

### Phase 1

_The highest priority features that form the core foundation — a deployable AWS network with remote state management._

- [x] **Network Foundation**
  - [x] **VPC & Subnets:** Provision a dedicated VPC with public subnets and routing tables, providing the network layer for the VPN server.
  - [x] **Default Security Group:** Lock down the default VPC security group to deny all traffic, enforcing explicit allow-rules only.
  - [x] **S3 Remote State Backend:** Bootstrap an S3 bucket with native locking for Terraform state, enabling safe, reproducible infrastructure management.

- [x] **Project Scaffolding**
  - [x] **Provider & Version Pinning:** Pin exact Terraform and AWS provider versions to ensure reproducible builds across environments.
  - [x] **Root Module Structure:** Establish the base config layout — locals.tf, providers.tf, versions.tf, and default tags — so all subsequent resources inherit consistent configuration.

---

### Phase 2

_With the network in place, deploy a working WireGuard server with secure key management._

- [x] **WireGuard Server Deployment**
  - [x] **EC2 Instance with Cloud-Init:** Launch an EC2 instance with WireGuard installed and configured automatically via user-data, eliminating manual server setup.
  - [x] **IAM Role & SSM Integration:** Create an IAM role granting the instance access to retrieve its WireGuard private key from SSM Parameter Store at boot.
  - [x] **Security Group Rules:** Open UDP 51820 for WireGuard tunnel traffic and use SSM Session Manager for instance access (no SSH port), providing a minimal-privilege network posture.
  - [x] **End-to-End Single-Client Tunnel:** Validate that a single WireGuard client can establish a tunnel and route traffic through the server.

---

### Phase 3

_Extend the server to support multiple clients and add quality gates for long-term maintainability._

- [x] **Multi-Client Support**
  - [x] **Configurable Client List:** Allow multiple WireGuard clients via a Terraform variable list, each with a unique public key and IP assignment.
  - [x] **Per-Client IP Allocation:** Assign each client a dedicated IP within the WireGuard subnet for clean routing and easy identification.

- [x] **Quality & Documentation**
  - [x] **Pre-Commit Hooks:** Integrate fmt, tflint, trivy, and terraform-docs as pre-commit checks to enforce code quality and catch security issues before merge.
  - [x] **User Journey Documentation:** Document the full clone-configure-deploy workflow so new users can go from zero to a working VPN with clear guidance.

---

### Phase 4

_Observability and proactive operations for the running VPN — a VPN-only web dashboard and alerting, so the operator can see and be told about problems without SSH._

- [x] **Web Dashboard (specs 002 / 003):** A VPN-only, read-only web dashboard showing server health, per-client online/offline status, throughput, and recent handshake events.
- [x] **Client Config Download (spec 004):** Download ready-to-use client configurations (full and split-tunnel) for a peer directly from the dashboard.
- [x] **Dashboard Distribution (spec 005):** Ship the dashboard as a verified public GitHub Release binary, pinned in Terraform and fetched + checksum-verified at first boot.
- [x] **Connection History & Geo Map (spec 006):** Per-client connection timeline (online/offline, session count, connected time) and an offline world map of peer locations.
- [~] **Alert Notifications (spec 007):** Push alerts to a Slack-compatible webhook when the service is down, disk/CPU is high sustained, a peer drops, or a peer crosses a transfer cap — edge-triggered with cooldown and recovery, opt-in, configurable thresholds. _(Code-complete; pending deploy config + manual end-to-end.)_
- [ ] **Runtime Webhook Management (spec 008):** Manage the alert webhook from the dashboard at runtime (set / test / revert), seeded from SSM at boot and never persisted. Includes the Terraform SSM→EnvironmentFile wiring that supplies the boot seed. _(Specified.)_

---

### Future / Under Consideration

_Not yet specified; captured so the direction isn't lost._

- **All-tabs UI/UX polish pass** — a coherent visual/layout review across every dashboard tab.
- **Repository open-sourcing** — finalize licensing, scope down committed permissions, and purge any historical secrets/large blobs before the repo goes public.
