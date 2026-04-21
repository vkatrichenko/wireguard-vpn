# Technical Specification: Network Foundation

- **Functional Specification:** `context/spec/001-network-foundation/functional-spec.md`
- **Status:** Completed
- **Author(s):** Poe

---

## 1. High-Level Technical Approach

The network foundation is largely implemented. This spec documents the existing architecture and defines six targeted changes:

1. **Add S3 public access block** to the state bucket (open acceptance criterion)
2. **Fix copy-paste bug** in VPC module `outputs.tf` (`database_subnets` returns wrong value)
3. **Remove EKS/Kubernetes tags** and `cluster_name` local from VPC module
4. **Make NAT gateway optional** via a new variable (default: disabled), keep private subnets
5. **Strip general SG to SSH-only** (remove ports 80, 443, 5000)
6. **Fix `ports` variable type** from `list(string)` to `list(number)`

All changes touch two areas: the backend root module (`terraform/dev/backend/`) and the network VPC module (`terraform/modules/network/vpc/`).

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1. S3 Public Access Block

**File:** `terraform/dev/backend/main.tf`

Add a new `aws_s3_bucket_public_access_block` resource referencing `aws_s3_bucket.terraform_state.id` with all four settings enabled:
- `block_public_acls = true`
- `block_public_policy = true`
- `ignore_public_acls = true`
- `restrict_public_buckets = true`

### 2.2. Fix `database_subnets` Output Bug

**File:** `terraform/modules/network/vpc/outputs.tf` (line 22)

Change the `database_subnets` output value from `module.vpc.intra_subnets` to `module.vpc.database_subnets`. This is a copy-paste error that would return incorrect subnet IDs if database subnets were ever enabled.

### 2.3. Remove EKS/Kubernetes Tags

**File:** `terraform/modules/network/vpc/main.tf`

- Remove `kubernetes.io/role/elb`, `kubernetes.io/role/internal-elb`, and `kubernetes.io/cluster/*` tags from both `public_subnet_tags` and `private_subnet_tags` in the VPC module call.

**File:** `terraform/modules/network/vpc/locals.tf`

- Remove the `cluster_name` local variable entirely.

### 2.4. Make NAT Gateway Optional

**File:** `terraform/modules/network/vpc/variables.tf`

- Add a new variable:
  - Name: `enable_nat_gateway`
  - Type: `bool`
  - Default: `false`
  - Description: "Enable a single NAT gateway for private subnet outbound internet access"

**File:** `terraform/modules/network/vpc/main.tf`

- Change `enable_nat_gateway = true` and `single_nat_gateway = true` to reference the new variable:
  - `enable_nat_gateway = var.enable_nat_gateway`
  - `single_nat_gateway = var.enable_nat_gateway`

**File:** `terraform/dev/main.tf`

- Pass `enable_nat_gateway = false` (or omit, since default is `false`) to disable the NAT gateway in the test environment.

**Impact:** This will destroy the existing NAT gateway on next apply (~$32/month savings). Private subnets remain but lose outbound internet access until the variable is flipped back to `true`.

### 2.5. Strip General SG to SSH-Only

**File:** `terraform/dev/main.tf`

- Change the `ports` argument from `[22, 80, 443, 5000]` to `[22]`.

### 2.6. Fix `ports` Variable Type

**File:** `terraform/modules/network/vpc/variables.tf`

- Change `ports` variable type from `list(string)` to `list(number)`.

---

## 3. Impact and Risk Analysis

### System Dependencies

- The **WireGuard module** depends on `module.network.vpc_id` and `module.network.public_subnets[0]`. None of the changes above modify public subnets or the VPC itself, so the WireGuard module is unaffected.
- The **backend module** change (public access block) is additive — it only restricts access further on an existing bucket.

### Potential Risks & Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| NAT gateway removal triggers replacement of resources in private subnets | No current resources in private subnets — no impact | Verify via `terraform plan` that only the NAT gateway and its route are destroyed |
| S3 public access block conflicts with account-level settings | None — bucket-level block is additive and cannot be less restrictive than account-level | Verify plan shows only the new resource, no modifications to existing bucket |
| Removing SG ports 80/443/5000 breaks existing services | No services use these ports — only WireGuard (UDP 51820, separate SG) is running | Confirm via plan that only the general SG rules change, not the WireGuard SG |
| EKS tag removal triggers subnet recreation | Tag changes are in-place updates, not replacements | Verify via plan that subnets show `~ update in-place`, not `- destroy` / `+ create` |

---

## 4. Testing Strategy

1. **`terraform fmt -recursive`** from repo root — ensures consistent formatting
2. **`terraform validate`** in both affected root modules:
   - `terraform/dev/` (network module changes)
   - `terraform/dev/backend/` (S3 public access block)
3. **`terraform plan -out=tfplan`** in both root modules — review output to confirm:
   - Only expected resources are modified/added/destroyed
   - No unintended replacements (especially subnets)
   - NAT gateway shows as destroyed, not replaced
   - S3 public access block shows as a new resource
4. **`make pre-commit`** at repo root — runs fmt, tflint, trivy, terraform-docs
