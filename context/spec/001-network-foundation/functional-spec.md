# Functional Specification: Network Foundation

- **Roadmap Item:** Network Foundation — VPC, subnets, security group, and S3 remote state backend
- **Status:** Draft
- **Author:** Poe

---

## 1. Overview and Rationale (The "Why")

The network foundation provides the core AWS infrastructure layer that the WireGuard server and all future resources are deployed into, plus the Terraform state backend for safe, reproducible state management.

**Problem it solves:** Deploying a VPN server into a default VPC or manually-created network is error-prone, not reproducible, and doesn't follow security best practices. A codified network ensures every deployment starts from a known-good, auditable state.

**Success measure:** The operator can run `terraform apply` and get a fully provisioned VPC with subnets, routing, NAT, and a security group — plus an S3 state backend — without any manual AWS console steps.

---

## 2. Functional Requirements (The "What")

### 2.1. VPC & Subnets

- The system provisions a dedicated VPC using the `terraform-aws-modules/vpc/aws` v6.0.0 public module.
  - **Acceptance Criteria:**
    - [x] A VPC is created with CIDR `10.23.0.0/16`.
    - [x] 3 public subnets are created across 3 AZs (us-east-1a/b/c): `10.23.1.0/24`, `10.23.2.0/24`, `10.23.3.0/24`.
    - [x] 3 private subnets are created across 3 AZs: `10.23.11.0/24`, `10.23.12.0/24`, `10.23.13.0/24`.
    - [x] An internet gateway is attached to the VPC with a default route (0.0.0.0/0) for public subnets.
    - [x] A single NAT gateway is provisioned in the first public subnet for cost optimization in test environment.
    - [x] Private subnets route outbound traffic through the NAT gateway.
    - [x] DNS hostnames and DNS support are enabled on the VPC.
    - [x] All subnets are tagged with `Role` (public/private) and Kubernetes-readiness tags.

### 2.2. Security Group

- A general-purpose security group is created for the VPC.
  - **Acceptance Criteria:**
    - [x] Security group named `wireguard-vpn-test-general` is created in the VPC.
    - [x] Ingress rules allow TCP ports 22, 80, 443, and 5000 from 0.0.0.0/0.
    - [x] Egress allows all traffic (protocol -1) to 0.0.0.0/0.

### 2.3. S3 Remote State Backend

- An S3 bucket is bootstrapped via a separate root module (`terraform/dev/backend/`) to store Terraform state.
  - **Acceptance Criteria:**
    - [x] S3 bucket `wireguard-vpn-test-tf-states` is created.
    - [x] Server-side encryption (AES-256) is enabled.
    - [x] Bucket versioning is enabled for state file recovery.
    - [x] Object lock is enabled for additional protection.
    - [x] `prevent_destroy = true` lifecycle rule prevents accidental deletion.
    - [x] The main root module uses this bucket as its S3 backend with native locking (`use_lockfile = true`).
    - [x] No DynamoDB lock table — state locking is handled natively by S3.
    - [ ] All public access is blocked via `aws_s3_bucket_public_access_block` with all four settings enabled.

---

## 3. Scope and Boundaries

### In-Scope

- VPC with CIDR `10.23.0.0/16` using `terraform-aws-modules/vpc/aws` v6.0.0
- 3 public + 3 private subnets across 3 AZs
- Internet gateway with public route table
- Single NAT gateway (cost-optimized for test)
- General security group with TCP 22/80/443/5000 ingress
- S3 state bucket with encryption, versioning, object lock, and prevent_destroy
- S3 public access block (to be added)
- Native S3 state locking

### Out-of-Scope

- **Project Scaffolding** (provider/version pinning, root module structure) — separate roadmap item
- **WireGuard Server Deployment** (EC2, IAM, WireGuard SG, user-data) — Phase 2 roadmap item
- **Multi-Client Support** — Phase 3 roadmap item
- **Quality & Documentation** — Phase 3 roadmap item
- Default VPC security group lockdown (accepted as-is)
- Database subnets, intra subnets
- VPC flow logs, CloudWatch monitoring
- VPC endpoints, Route53 DNS
- DynamoDB lock table
- CI/CD pipeline
