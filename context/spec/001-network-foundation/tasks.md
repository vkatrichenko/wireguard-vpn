# Tasks: Network Foundation

_Vertically sliced task list. Each slice leaves Terraform in a valid, plannable state._

---

- [ ] **Slice 1: S3 Backend Hardening**
  - [x] Add `aws_s3_bucket_public_access_block` resource to `terraform/dev/backend/main.tf` referencing `aws_s3_bucket.terraform_state.id` with all four settings enabled (`block_public_acls`, `block_public_policy`, `ignore_public_acls`, `restrict_public_buckets`). **[Agent: terraform-aws]**
  - [ ] Run `terraform validate` in `terraform/dev/backend/` to confirm the module is syntactically valid. **[Agent: terraform-aws]**
  - [ ] Run `terraform plan -out=tfplan` in `terraform/dev/backend/` — confirm only the new `aws_s3_bucket_public_access_block` resource is added; no changes to existing bucket, encryption, or versioning resources. **[Agent: terraform-aws]**

---

- [ ] **Slice 2: VPC Module Cleanup**
  - [ ] Fix `database_subnets` output in `terraform/modules/network/vpc/outputs.tf` (line 22): change value from `module.vpc.intra_subnets` to `module.vpc.database_subnets`. **[Agent: terraform-aws]**
  - [ ] Remove all `kubernetes.io/*` tags from `public_subnet_tags` and `private_subnet_tags` in `terraform/modules/network/vpc/main.tf`. **[Agent: terraform-aws]**
  - [ ] Remove the `cluster_name` local variable from `terraform/modules/network/vpc/locals.tf`. **[Agent: terraform-aws]**
  - [ ] Fix `ports` variable type from `list(string)` to `list(number)` in `terraform/modules/network/vpc/variables.tf`. **[Agent: terraform-aws]**
  - [ ] Run `terraform validate` in `terraform/dev/` to confirm the root module is syntactically valid after all changes. **[Agent: terraform-aws]**
  - [ ] Run `terraform plan -out=tfplan` in `terraform/dev/` — confirm subnets show `~ update in-place` (tag removal only), no `- destroy` / `+ create` on any subnet or VPC resource. **[Agent: terraform-aws]**

---

- [ ] **Slice 3: NAT Gateway + SG Hardening**
  - [ ] Add `enable_nat_gateway` variable (`type = bool`, `default = false`, description: "Enable a single NAT gateway for private subnet outbound internet access") to `terraform/modules/network/vpc/variables.tf`. **[Agent: terraform-aws]**
  - [ ] Update VPC module call in `terraform/modules/network/vpc/main.tf`: set `enable_nat_gateway = var.enable_nat_gateway` and `single_nat_gateway = var.enable_nat_gateway`. **[Agent: terraform-aws]**
  - [ ] Strip `ports` argument in `terraform/dev/main.tf` from `[22, 80, 443, 5000]` to `[22]`. **[Agent: terraform-aws]**
  - [ ] Run `terraform validate` in `terraform/dev/` to confirm the root module is syntactically valid. **[Agent: terraform-aws]**
  - [ ] Run `terraform plan -out=tfplan` in `terraform/dev/` — confirm: NAT gateway and its route are destroyed, SG ingress rules for ports 80/443/5000 are removed, no subnet replacements, WireGuard module resources are unchanged. **[Agent: terraform-aws]**

---

- [ ] **Final Verification**
  - [ ] Run `terraform fmt -recursive` from repo root to ensure consistent formatting across all files. **[Agent: devsecops-quality]**
  - [ ] Run `make pre-commit` from repo root — all checks (fmt, tflint, trivy, terraform-docs) must pass. **[Agent: devsecops-quality]**
