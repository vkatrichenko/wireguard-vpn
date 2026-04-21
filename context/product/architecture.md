# System Architecture Overview: wireguard-vpn

---

## 1. Infrastructure & Provisioning

- **Infrastructure as Code:** Terraform 1.14.8 (exact pin, no ranges)
- **Cloud Provider:** AWS via `hashicorp/aws` provider 6.41.0 (exact pin)
- **Target Region:** us-east-1
- **State Backend:** S3 with native locking (`use_lockfile = true`), no DynamoDB
- **Module Strategy:** Local module paths (`../modules/...`); versioned git refs planned for cross-repo reuse

---

## 2. Network & Security

- **VPC Topology:** Single VPC with one public subnet — no NAT gateway, no private subnets
- **Routing:** Internet gateway with a default route in the public subnet route table
- **Default Security Group:** Locked down to deny all traffic; explicit allow-rules only
- **WireGuard Traffic:** UDP 51820 open to 0.0.0.0/0 via dedicated security group
- **Instance Access:** SSM Session Manager only — no SSH port (22) exposed
- **VPN Protocol:** WireGuard (kernel-level, UDP-based, modern cryptography)

---

## 3. Compute & Configuration

- **Instance Type:** t3.micro (2 vCPU, 1 GB RAM, free-tier eligible)
- **AMI:** Explicitly pinned in locals.tf (no `most_recent = true` data source)
- **Configuration Method (Current):** Cloud-init user-data — installs and configures WireGuard at first boot, retrieves private key from SSM
- **Alternatives Under Evaluation:**
  - **Packer** — pre-baked AMI with WireGuard installed; faster boot, immutable infrastructure pattern; adds a build pipeline step
  - **Ansible** — post-provisioning configuration; allows re-configuration without instance replacement; adds tool dependency
- **Client Management:** Configurable `clients_config` list in Terraform, each entry with a unique public key and IP assignment

---

## 4. Secrets & State Management

- **Server Private Key:** AWS SSM Parameter Store (SecureString) at `/config/wireguard/default-private-key`, created manually outside Terraform
- **Client Keys:** Generated off-host (`wg genkey | tee privatekey | wg pubkey > publickey`), public keys added to Terraform config
- **IAM Access:** Instance role with scoped SSM `GetParameter` permission for the private key parameter
- **Terraform State:** S3 bucket `wireguard-vpn-test-tf-states` with `prevent_destroy = true`, bootstrapped via separate root module (`terraform/dev/backend/`)

---

## 5. Code Quality & Tooling

- **Pre-Commit Framework:** `antonbabenko/pre-commit-terraform` v1.105.0 (runs in Docker via `make pre-commit`)
- **Formatting:** `terraform fmt -recursive`
- **Linting:** tflint
- **Security Scanning:** Trivy (currently warn-only, `--exit-code=0`; HIGH/CRITICAL findings treated as real)
- **Documentation:** terraform-docs (auto-generated module docs)
- **Validation:** `terraform validate` in every affected root module before claiming done
