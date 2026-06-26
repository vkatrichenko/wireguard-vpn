# Contributing

Thanks for your interest in `wireguard-vpn` — a self-hosted WireGuard VPN server on
AWS EC2, provisioned with Terraform, plus a small Go dashboard. The project is
solo-maintained, so contributions, bug reports, and ideas are all welcome.

## How to propose changes

For anything non-trivial, **open an issue first** so the change can be discussed
before you invest time in it. Small, obvious fixes (typos, docs, one-line bugfixes)
can go straight to a pull request.

This repo plans its features with the **awos workflow**, and you'll see the trail of
that under `context/spec/`. Each feature lives in `context/spec/NNN-<slug>/` and
moves through four documents:

1. `functional-spec.md` — what the feature does for the user (the "what" and "why").
2. `technical-considerations.md` — how it will be built (the "how").
3. `tasks.md` — the work broken into engineer-sized tasks.
4. Implementation, then verification against the acceptance criteria.

You don't have to author a full spec for a small change, but understanding this
layout helps when reading existing `context/spec/` directories or proposing a larger
feature.

## Local development setup

There are two halves to the project. You only need the toolchain for the part you're
touching.

### Infrastructure (Terraform)

- **Terraform `= 1.14.8`** (pinned exactly — see below).
- An AWS account and credentials. Every `terraform` and `aws` command in this repo
  expects `AWS_PROFILE=csm` in the environment:

  ```bash
  export AWS_PROFILE=csm
  ```

- Day-to-day commands run from `terraform/dev/`:

  ```bash
  terraform init
  terraform validate
  terraform plan -out=tfplan
  ```

### Dashboard (Go)

- **Go `1.25`** and `make`.
- Build, run, and test from the `dashboard/` directory:

  ```bash
  cd dashboard
  make build
  make test
  ```

### Docker / OrbStack

The quality gate (`make pre-commit`) runs in a pinned Docker container, so you'll
need Docker (OrbStack works well). On **Apple Silicon**, pass
`--platform linux/amd64` for any image you build locally to match the deploy target.

## Quality gate

Before opening a pull request, run the quality gate from the **repository root**:

```bash
make pre-commit
```

This runs, inside a pinned container
(`ghcr.io/antonbabenko/pre-commit-terraform`):

- `terraform fmt` — formatting.
- `terraform-docs` — module docs.
- `tflint` — Terraform linting.
- `trivy` — security scanning.

Trivy is currently **warn-only** (`--exit-code=0`), so it will not fail the run — but
**HIGH and CRITICAL findings must be treated as real**, not noise. Don't open a PR
that introduces new HIGH/CRITICAL findings without addressing or explicitly
justifying them.

## Commit and branch conventions

- **Conventional Commits.** Prefix every commit with one of:
  `feat:`, `fix:`, `infra:`, `docs:`, `refactor:`, `chore:`.
- **Branch names** use the matching prefix, lowercase-with-hyphens — e.g.
  `feat/add-cors-headers`, `fix/peer-handshake-timeout`.
- **One logical change per commit.** Keep commits focused and reviewable.

## Terraform conventions

These are enforced, not optional:

- **Exact version pinning only.** Never `~>` or version ranges. Current pins:
  `terraform = "= 1.14.8"`, `aws = "= 6.41.0"`.
- **Four required tags** on every resource, applied via the provider's
  `default_tags`: `Environment`, `Project`, `Owner`, `Managed`. Don't re-tag
  individual resources unless you're adding a `Name`.
- **Root configuration lives in `locals.tf`**, never `terraform.tfvars`.
- **Apply workflow** is always:

  ```bash
  terraform plan -out=tfplan   # produce a plan
  # human review of the diff
  terraform apply tfplan       # apply the reviewed plan
  ```

  Never run a bare `terraform apply`.

**Apply is manual and owner-run only.** There is **no CI/CD**, and Terraform is never
applied automatically. Do not propose automated apply pipelines.

## No CI

All checks run locally. There is no continuous-integration pipeline — the
maintainer and reviewers run `make pre-commit` and the Terraform checks by hand. When
you open a PR, you're expected to have run the quality gate locally first.

## Pull request descriptions

Follow the repository's PR template (a `.github/PULL_REQUEST_TEMPLATE.md` is provided
in the repo). PR descriptions should include:

- **Summary** — a bullet list of the concrete changes, each written in the
  imperative ("Add X", "Refactor Y"), leading with the outcome.
- **Architecture decisions** *(when applicable)* — a short paragraph per non-obvious
  decision, explaining the *why* and any trade-offs.
- **A surface-area table** describing what changed (files/components, resources, or
  workflows — whichever fits).

Keep the title short and verb-led; details belong in the body.

## Using Claude Code

The committed `.claude/settings.json` **intentionally grants no broad auto-approve
permissions** — it ships safe by default for everyone who clones the repo. If you use
Claude Code and want more permissive auto-approvals for your own workflow, put them in
`.claude/settings.local.json`, which is **gitignored**. Never add personal or
permissive permissions to the committed `settings.json`.

## License

By contributing to this project, you agree that your contributions will be licensed
under the project's **Apache License 2.0** (see [`LICENSE`](./LICENSE)).
