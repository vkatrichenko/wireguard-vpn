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
- [x] **Alert Notifications (spec 007):** Push alerts to a Slack-compatible webhook when the service is down, disk/CPU is high sustained, a peer drops, or a peer crosses a transfer cap — edge-triggered with cooldown and recovery, opt-in, configurable thresholds. _(Deployed; operator-verified delivery on 2026-06-25.)_
- [x] **Runtime Webhook Management (spec 008):** Manage the alert webhook from the dashboard at runtime (set / test / revert), seeded from SSM at boot and never persisted. Includes the Terraform SSM→EnvironmentFile wiring that supplies the boot seed. _(Deployed; operator-verified Set/Test on 2026-06-25.)_

---

### Phase 5

_Dashboard design & legibility._

- [x] **Dashboard Design System & Responsive Refresh (spec 009):** A cohesive token-driven design system (embedded IBM Plex fonts, amber-on-graphite "precision instrument" palette), fluid responsiveness from phone to ultrawide, restyled components + subtle motion, and WCAG-AA accessibility — applied across all 6 tabs. _(Deployed & operator-verified 2026-06-25.)_
- [x] **Geo Map Zoom & Legibility (spec 010):** Fix the oversized marker + empty-state defects and add bounded zoom/pan (buttons + wheel/pinch/drag) to the offline peer map so a peer's country is readable. _(Deployed & operator-verified 2026-06-25.)_

---

### Phase 6

_Preparing the project to be published as open source._

- [x] **Open-Source Readiness (spec 011):** Apache-2.0 `LICENSE` + `NOTICE` attribution, `SECURITY.md` (GitHub private advisories), `CONTRIBUTING.md`, GitHub issue/PR templates, and repo hygiene — scoped-down committed agent permissions + `.gitignore` hardening (`*.mmdb` / `*.tfplan` / `tfplan`). _(Deployed to main 2026-06-26. The git-history blob purge was descoped; CI is a separate future effort.)_

---

### Phase 7

_Alerting fan-out & external observability._

- [~] **Alert Transports & Prometheus Metrics (spec 012):** Fan out alert delivery to opt-in Slack bot (`chat.postMessage`), Telegram, and Discord transports alongside the runtime-managed Slack incoming webhook (a `MultiNotifier` composite that isolates and aggregates per-transport failures); add a hand-rolled Prometheus `GET /metrics` endpoint (VPN-only, no auth, current in-memory values only, no client library, no per-scrape exec/DB); and remove the noisy peer-down/stale-peer alert condition (five → four conditions). Terraform seeds the new transport secrets from SSM (opt-in, empty-default → no behavior change when unconfigured). _(Terraform applied 2026-06-28; the dashboard binary release + live delivery/scrape E2E remain owner-run post-deploy.)_

---

### Phase 8

_Portability — architecture choice & deployment beyond Terraform/AWS._

- [x] **ARM64 / AMD64 Architecture Option (spec 013):** Make host CPU architecture a single toggle (`cpu_architecture`, owned by the `wireguard` module) that derives the AMI suffix, AMI `architecture` filter, and default instance type, with `arm64` (Graviton `t4g.micro`) as the default; a dual-arch dashboard release (`amd64` + `arm64` + one `SHA256SUMS`); and architecture-agnostic boot (runtime `uname -m` → matching AWS CLI + `wireguard-dashboard-$GOARCH`, checksum-verified, fail-hard on mismatch). _(Deployed & operator-verified 2026-06-29: `terraform apply` default stood up an arm64/t4g instance with tunnel + all dashboard tabs working.)_
- [x] **Standalone Install Script (spec 014):** Extract the portable WireGuard + optional-dashboard bootstrap into a committed, env-driven, Ubuntu-only `scripts/install.sh` (fail-hard, shellcheck-clean), usable on any plain Ubuntu VPS via download-then-run; refactor the EC2 user-data into a thin AWS wrapper that fetches the same script from raw GitHub at a content-pinned (`sha256`) ref and runs it, so the AWS and VPS paths can't drift. _(EC2 path operator-verified 2026-06-29 via the arm64 deploy; standalone-VPS runtime branches code-complete + shellcheck-clean but not yet VPS-runtime-tested — see spec tasks.)_

---

### Phase 9

_Runtime client management & first-client onboarding — manage peers live and make the standalone path operable end-to-end._

- [x] **Runtime Client Management (spec 015):** Make the dashboard's on-box SQLite the runtime source of truth for peers — add/remove/edit clients live from the UI (paste-public-key), applied with `wg syncconf` (no instance replacement, no tunnel drop), with Terraform `clients_config` demoted to a first-boot seed plus an export + drift indicator for git reconciliation; identical on EC2 and standalone VPS. _(Deployed & operator-verified.)_
- [x] **First-Client Onboarding & Dashboard Usability Fixes (spec 016):** Print an example client config in `install.sh`'s success output (bootstraps the first peer); inline full-width client editing (replacing the cramped drawer); a human-readable handshakes panel (names resolved from the live client DB, one row per peer); and a full `install.sh` install / update / remove / purge lifecycle with safe no-clobber updates (preserve peers + server key). _(Deployed & operator-verified 2026-06-30; the live edit button + stable reruns required the post-v0.0.10 follow-ups — capture-phase toggle + server-key persistence, PR #48.)_

---

### Phase 10

_GitOps peer management — declare the peer set in Terraform and reconcile it live._

- [x] **Terraform-Managed Peers via REST API (spec 017):** Make git/Terraform authoritative for the WireGuard peer set by driving the dashboard's new idempotent `PUT /api/clients` bulk endpoint (SQLite → `wg syncconf`, no tunnel drop) through the `Mastercard/restapi` provider (`= 3.0.0`); the whole set is one count-gated `restapi_object` (flag `manage_peers_via_api`, off by default) so UI edits and UI-only peers surface as `terraform plan` drift and `apply` reconciles to git; a canonical address-sorted export read avoids phantom drift; the spec-015 drift badge is repointed to a dashboard-owned SQLite baseline. _(Implemented & owner-verified live 2026-07-01, dashboard v0.0.12. Two ergonomics/safety footguns surfaced — destructive empty-PUT on destroy/flag-off, and zero-peer cold-start on rebuild — deferred to the follow-up client-management-mode spec, not defects in the mechanism.)_

- [x] **Client Management Mode — local | cloud (spec 018):** Replace spec-017's `manage_peers_via_api` bool with a single `client_management_mode` (`local` | `cloud`, default `local`). `local` = SQLite-only (spec 015). `cloud` = a versioned S3 object as a UI-authoritative, durable bridge — the dashboard reads it at boot and rewrites it on every UI edit (live, no instance churn), Terraform seeds it once from `clients_config` and surfaces divergence via a warn-only `check` (never reverts); resolves both spec-017 footguns (empty-PUT wipe, zero-peer cold-start) with no live REST-API path. _(Implemented & owner-validated live 2026-07-02, dashboard v0.0.15; two post-deploy bugs found & fixed — empty-S3 wipe (PR #53) and `aws`-not-in-PATH/storeReady latch (PR #54). Practical learning: in cloud mode, editing `clients_config` in git still churns the instance and creates drift while the box reads peers from S3 — motivating a follow-up refactor to a UI-first model with an admin bootstrap peer and S3-as-backup.)_

---

### Phase 11

_UI-first peer management — collapse to a single authority and make first-peer onboarding shell-native._

- [x] **UI-First Client Management (spec 019):** Supersede spec-018's declarative-`clients_config` + S3-bridge with a single UI-authoritative model. Remove `clients_config` and the warn-only S3 drift `check` entirely — the dashboard UI (and an equivalent on-box `wg-peer` script) is the **only** peer-management authority, so peer add/edit/remove never replaces the instance nor shows as `terraform plan` drift. Terraform seeds exactly one `admin_peer` (name + **public** key, nullable) for anti-lockout, only when the store is empty. `local` = SQLite-only; `cloud` = SQLite + S3 as a **pure durable backup** (write-through + boot restore, no TF reads, no drift), keeping the PR #53/#54 hardening (empty ≠ authoritative, `storeReady` guard, best-effort write-through, self-heal). The installer always deploys the dashboard with WireGuard (WG-only path removed); the spec-015/016 drift badge + HCL/tfvars export are gone. New optional `scripts/wg-peer` CLI (`add`/`remove`/`update`) drives the local dashboard API (script ≡ UI), with server-side ephemeral keygen and a `--show-config` flag for first-peer onboarding on a manual VPS. _(Deployed & operator-verified live 2026-07-03, dashboard v0.0.16. One cold-seed bug found & fixed during deploy: the dashboard now creates `clients.json` itself, so a first-boot `get-object` on the missing key returned 403 (not 404) without `s3:ListBucket`, latching `storeReady=false` and silently disabling the backup — fixed by granting `s3:ListBucket` on the bucket.)_

### Phase 12

_Hardening & server-key automation — remove operator key handling, close a data-loss path, shrink the security blast radius._

- [x] **Hardening & Server-Key Automation (spec 020):** Three workstreams shipped together. **(A) Dashboard:** repoint the client detail/history views off the static Terraform boot manifest to the runtime DB (they 404'd for UI-added clients since spec 019); give the cloud-mode S3 backup a self-heal recheck on the hourly poller tick plus a health signal (`GET /api/health` `client_store_ready`, cloud-only, + an About-tab badge) — closing a silent stale-restore data-loss path. **(B) Server key:** the instance now self-manages its WireGuard server private key — read-from-SSM or generate-and-store at boot — so the operator never handles it and the key never enters Terraform state or the launch template. Chosen design: the private key is **instance-owned** (no `aws_ssm_parameter` resource — a TF-managed one reads `value` back into state on refresh even behind `ignore_changes`, defeating the goal); IAM uses a constructed ARN; the existing param is adopted with no rotation. Only the non-secret **public** key is TF-managed (published to SSM for pre-connect retrieval). The instance IAM role is decoupled from the `use_eip` toggle (a latent bug) via `moved` blocks. **(C) Hardening:** IMDSv2-required, root-EBS encryption, SSH removed entirely (port 22 + all key material → SSM Session Manager only, which also purged the SSH PEM from state), health-check bucket posture, state bucket → AWS-managed KMS, and removal of a no-op `ignore_changes`, an orphan world-open SG, a dead Go sentinel, and a fragile `404` match. _(Deployed & operator-verified live 2026-07-03. One accepted deviation: the state bucket's `object_lock_enabled` flag is left as-is — it's Forces-new-resource on a `prevent_destroy` bucket, so it can't be dropped without replacing the state bucket.)_

---

### Future / Under Consideration

_Not yet specified; captured so the direction isn't lost._

- **Repository open-sourcing — remaining work** — flip the repo to public; the optional git-history purge of the ~65 MB GeoLite2 blob (descoped from spec 011); the deferred `tfplan` / server-key history exposure; and CI + branch-protection so PR checks pass (the recurring `mergeable_state: blocked`).
