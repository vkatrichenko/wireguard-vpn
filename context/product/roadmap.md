# Product Roadmap: wireguard-vpn

_This roadmap outlines our strategic direction based on user needs and project goals. It focuses on the "what" and "why," not the technical "how."_

---

### Phase 1

_The highest priority features that form the core foundation — a deployable AWS network with remote state management._

- [x] **Network Foundation**
  - [x] **VPC & Subnets:** Provision a dedicated VPC with public subnets and routing tables, providing the network layer for the VPN server.
  - [x] **Default Security Group:** Lock down the default VPC security group to deny all traffic, enforcing explicit allow-rules only.
  - [x] **S3 Remote State Backend:** Bootstrap an S3 bucket with native locking for Terraform state, enabling safe, reproducible infrastructure management.

- [ ] **Project Scaffolding**
  - [ ] **Provider & Version Pinning:** Pin exact Terraform and AWS provider versions to ensure reproducible builds across environments.
  - [ ] **Root Module Structure:** Establish the base config layout — locals.tf, providers.tf, versions.tf, and default tags — so all subsequent resources inherit consistent configuration.

---

### Phase 2

_With the network in place, deploy a working WireGuard server with secure key management._

- [ ] **WireGuard Server Deployment**
  - [ ] **EC2 Instance with Cloud-Init:** Launch an EC2 instance with WireGuard installed and configured automatically via user-data, eliminating manual server setup.
  - [ ] **IAM Role & SSM Integration:** Create an IAM role granting the instance access to retrieve its WireGuard private key from SSM Parameter Store at boot.
  - [ ] **Security Group Rules:** Open UDP 51820 for WireGuard tunnel traffic and restrict SSH access, providing a minimal-privilege network posture.
  - [ ] **End-to-End Single-Client Tunnel:** Validate that a single WireGuard client can establish a tunnel and route traffic through the server.

---

### Phase 3

_Extend the server to support multiple clients and add quality gates for long-term maintainability._

- [ ] **Multi-Client Support**
  - [ ] **Configurable Client List:** Allow multiple WireGuard clients via a Terraform variable list, each with a unique public key and IP assignment.
  - [ ] **Per-Client IP Allocation:** Assign each client a dedicated IP within the WireGuard subnet for clean routing and easy identification.

- [ ] **Quality & Documentation**
  - [ ] **Pre-Commit Hooks:** Integrate fmt, tflint, trivy, and terraform-docs as pre-commit checks to enforce code quality and catch security issues before merge.
  - [ ] **User Journey Documentation:** Document the full clone-configure-deploy workflow so new users can go from zero to a working VPN with clear guidance.
