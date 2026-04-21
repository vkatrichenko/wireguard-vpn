# wireguard-vpn

A self-hosted WireGuard VPN server on AWS EC2, provisioned with Terraform.

**Audience:** solo-maintained today, planned to be open-sourced later. Prefer documenting the *why* alongside the *what* so future external readers have context.

## Repo layout

```
terraform/
├── dev/                  # Root module — the deployable environment
│   ├── backend/          # Bootstrap: provisions the S3 state bucket (run once)
│   ├── locals.tf         # Environment config (region, name, CIDR, tags)
│   ├── providers.tf      # AWS provider + S3 backend config
│   ├── versions.tf       # Pinned terraform + provider versions
│   ├── datasource.tf     # AMI lookup
│   └── main.tf           # Composes network + wireguard modules
└── modules/
    ├── network/vpc/      # VPC, subnets, routing, default security group
    └── wireguard/        # EC2 + IAM + SG + SSM private key + user-data
```

Only `terraform/dev/` is a root module today, and no other environments are planned in the near term. Still, modules under `terraform/modules/` must stay environment-agnostic (accept `env`, `project_name`, `tags` as inputs; no hardcoded environment names) so adding a second root later is a copy-and-retune exercise, not a refactor.

**AWS target:** region `us-east-1`, credentials via `AWS_PROFILE=csm`. Every `terraform` and `aws` command run against this repo should have `AWS_PROFILE=csm` in the environment.

**Region is duplicated on purpose.** It appears in both [locals.tf](terraform/dev/locals.tf) (for the provider) and the S3 backend block in [providers.tf](terraform/dev/providers.tf). Terraform backend blocks cannot reference locals or variables — there's no way to DRY this up. If you ever change region, change it in both places and re-run `terraform init -migrate-state`.

## Conventions (enforced, not optional)

These override any general defaults:

- **Versions** — pin **exact**, never `~>` or ranges. Current: `terraform = "= 1.14.8"`, `aws = "= 6.41.0"`.
- **Tagging** — provider-level `default_tags` in [providers.tf](terraform/dev/providers.tf) applies four tags to every resource: `Environment`, `Project`, `Owner`, `Managed`. Do not re-tag individually unless adding a `Name`.
- **Root config lives in `locals.tf`**, never `terraform.tfvars`. See [locals.tf](terraform/dev/locals.tf).
- **Apply workflow** — always `terraform plan -out=tfplan` → human review → `terraform apply tfplan`. Never bare `apply`.
- **State locking** — uses native S3 locking via `use_lockfile = true` (Terraform ≥1.10). There is intentionally **no DynamoDB lock table** — do not add one, and do not be confused by its absence.
- **Destroy is forbidden by default.** Never run `terraform destroy` without explicit owner confirmation. Never `-target` a stateful resource (EIP, security group rules, IAM role, S3 state bucket) for deletion. The state bucket has `prevent_destroy = true` for a reason.
- **AMIs are pinned explicitly** in `locals.tf` (not resolved via `most_recent = true` data source) so AMI rotation becomes an explicit, reviewable commit.
- **Module sources** — local paths (`../modules/...`) for now. Will migrate to versioned git refs if/when modules are reused across repos.
- **Block ordering** inside resources: `count`/`for_each` → args → `tags` → `depends_on` → `lifecycle`. Variables: `description` → `type` → `default` → `validation`.

## External state (managed outside Terraform)

Claude should **not** try to create or manage these — assume they exist:

- SSM parameter `/config/wireguard/default-private-key` — the server's WireGuard private key, created manually via `aws ssm put-parameter`.
- S3 state bucket `wireguard-vpn-test-tf-states` — bootstrapped separately via [terraform/dev/backend/](terraform/dev/backend/). Has `prevent_destroy = true`.
- WireGuard **client** public keys — added by hand to the `clients_config` list in [main.tf](terraform/dev/main.tf). Generated off-host with `wg genkey | tee privatekey | wg pubkey > publickey`.

## Commands

All commands assume `AWS_PROFILE=csm` is set (export it once per shell, or prefix each command).

```bash
export AWS_PROFILE=csm

# Day-to-day, from terraform/dev/
terraform init
terraform validate
terraform plan -out=tfplan
terraform apply tfplan    # run only by the repo owner, never automated

# From repo root — runs fmt, docs, tflint, trivy in a Docker container
make pre-commit
```

**Apply is manual and local only.** There is no CI/CD pipeline and no plan to add one. Claude must not propose running `terraform apply` automatically, and must not suggest CI-based apply workflows unless explicitly asked.

### First-time backend bootstrap (fresh clone)

The S3 state bucket has to exist *before* `terraform/dev/` can init with its remote backend. Order of operations on a fresh clone:

```bash
export AWS_PROFILE=csm

# 1. Provision the state bucket using LOCAL state
cd terraform/dev/backend
terraform init
terraform plan -out=tfplan
terraform apply tfplan

# 2. Now the bucket exists — the main config can use it
cd ..
terraform init
```

The `backend/` directory is itself a (tiny) root module with its own local state. Do not commit that local `terraform.tfstate` — [.gitignore](.gitignore) should already exclude it.

Pre-commit is configured in [.pre-commit-config.yaml](.pre-commit-config.yaml) and pinned to `antonbabenko/pre-commit-terraform:v1.105.0`. Trivy is currently **warn-only** (`--exit-code=0`) — treat HIGH/CRITICAL findings as real, not noise.

## Before claiming work is done

1. `terraform fmt -recursive` from the repo root.
2. `terraform validate` in **every** root module the change touches. Both `terraform/dev/` and `terraform/dev/backend/` are root modules — if a change affects backend bootstrap, validate there too.
3. `terraform plan -out=tfplan` in the affected root module and read the diff — confirm resource counts and any replacements are expected. Share the plan summary in the response.
4. `make pre-commit` at the repo root to run fmt + docs + tflint + trivy on the whole tree. Run it for dev-level changes too, not just module changes.

Do not claim "done" without plan output. If infra can't be end-to-end tested in the session (e.g., you changed EC2 user-data but didn't actually apply + SSH in), say so explicitly — don't claim the WireGuard service will come up just because the plan succeeded.

## Workflow Orchestration

### 1. Plan Mode Default

- Enter plan mode for ANY non-trivial task (3+ steps or architectural decisions)
- If something goes sideways, STOP and re-plan immediately — don't keep pushing
- Use plan mode for verification steps, not just building
- Write detailed specs upfront to reduce ambiguity

### 2. Subagent Strategy

- Use subagents liberally to keep main context window clean
- Offload research, exploration, and parallel analysis to subagents
- For complex problems, throw more compute at it via subagents
- One task per subagent for focused execution

### 3. Self-Improvement Loop

- After ANY correction from the user: update `tasks/lessons.md` with the pattern
- Write rules for yourself that prevent the same mistake
- Ruthlessly iterate on these lessons until mistake rate drops
- Review lessons at session start for relevant project

### 4. Verification Before Done

- Never mark a task complete without proving it works
- Diff behavior between main and your changes when relevant
- Ask yourself: "Would a staff engineer approve this?"
- Run tests, check logs, demonstrate correctness
- For the concrete Terraform checklist, see **Before claiming work is done** above

### 5. Demand Elegance (Balanced)

- For non-trivial changes: pause and ask "is there a more elegant way?"
- If a fix feels hacky: "Knowing everything I know now, implement the elegant solution"
- Skip this for simple, obvious fixes — don't over-engineer
- Challenge your own work before presenting it

### 6. Autonomous Bug Fixing

- When given a bug report: just fix it. Don't ask for hand-holding
- Point at logs, errors, failing tests — then resolve them
- Zero context switching required from the user
- Go fix failing CI tests without being told how

## Task Management

1. **Plan First**: Write plan to `tasks/todo.md` with checkable items
2. **Verify Plan**: Check in before starting implementation
3. **Track Progress**: Mark items complete as you go
4. **Explain Changes**: High-level summary at each step
5. **Document Results**: Add review section to `tasks/todo.md`
6. **Capture Lessons**: Update `tasks/lessons.md` after corrections

## Core Principles

- **Simplicity First**: Make every change as simple as possible. Impact minimal code.
- **No Laziness**: Find root causes. No temporary fixes. Senior developer standards.
- **Minimal Impact**: Changes should only touch what's necessary. Avoid introducing bugs.

## Gotchas

- The WireGuard EC2 instance reads its private key from SSM at boot via [user-data](terraform/modules/wireguard/templates/user-data.txt). If the SSM parameter is missing or the IAM role lacks access, the service will fail silently — check `/var/log/cloud-init-output.log` on the instance.
- Cloud-init runs user-data as root. Do not add `sudo` inside user-data scripts.
- `locals.environment = "test"` — the `dev/` directory name and the `Environment` tag value intentionally differ. Tag value is the source of truth.

## Skills and MCPs Claude should use here

These are available globally — CLAUDE.md pins the expectation that they're used on this repo.

**Skills (invoke proactively):**
- `terraform-conventions` / `terraform-skill` — for any HCL authoring or review. Enforces the exact-pin / tagging / block-ordering rules above.
- `devops-iac-engineer` — for architecture-shaped questions (subnet layout, NAT, security posture, tool choice).
- `superpowers:verification-before-completion` — dovetails with the "Before claiming work is done" checklist; invoke before any "done" claim on infra changes.
- `superpowers:systematic-debugging` — use when the VPN isn't functioning or a plan fails in a way that isn't obvious. Don't propose fixes before understanding the failure.

**MCPs (required, not optional):**
- `terraform` MCP — **before writing or updating any HCL**, call `get_latest_provider_version` / `get_provider_details` to confirm the provider version and resource schemas. Never guess provider arguments from training data.
- `context7` — for WireGuard / `wg-quick` / systemd unit documentation, since the VPN tunnel config is outside Terraform's domain.
- `aws-knowledge-mcp-server` — when it becomes available in this project (currently a known gap). Until then, for AWS service specifics, be explicit that you're reasoning from training data and ask the owner to verify.

## TODO — owner to fill in

- AWS account ID the `csm` profile points at (useful for cross-checking plans).
- Commit message style (Conventional Commits vs. plain) and whether PRs are required before merge — relevant once the repo goes open-source.
