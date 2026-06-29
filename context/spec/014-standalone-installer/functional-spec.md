# Functional Specification: Standalone Install Script

- **Roadmap Item:** "Standalone installer (Spec D)" (promoting from _Future / Under Consideration_)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today the only way to stand up this WireGuard server + dashboard is a full `terraform apply` against AWS. That's right for the codified-infra audience, but it locks out anyone who already has a plain Ubuntu VPS (DigitalOcean, Hetzner, a home server) and just wants the same server + dashboard without adopting Terraform or AWS. The install logic _already exists_ — it's inside the EC2 `user-data` — but it's welded to AWS (SSM-injected key, `templatefile()` config, IMDSv2, an S3 readiness signal), so it can't be reused off-AWS.

This adds a single committed `scripts/install.sh` — a one-shot, env-var-driven, Ubuntu-only bootstrap that stands up the WireGuard server and (optionally) the dashboard on any Ubuntu host. The **same script** becomes the source of truth for the EC2 path: the user-data is refactored into a thin AWS wrapper that sets the environment and runs the script, so the two install paths can no longer drift.

**Success looks like:** an operator on a fresh Ubuntu VPS downloads `install.sh`, reviews it, runs `sudo bash install.sh`, and ends up with a working WireGuard tunnel and a reachable dashboard — and the EC2 deploy still produces a byte-for-byte-equivalent working box from the same script.

**Non-goal:** this is not a client manager (à la angristan) and not a multi-distro installer — see §3.

---

## 2. Functional Requirements (The "What")

### 2.1 One-shot server + dashboard bootstrap

- **As a VPS operator, I want** to run one script and get a working WireGuard server, **so that** I don't have to wire up WireGuard, NAT, forwarding, and a systemd service by hand.
  - **Acceptance Criteria:**
    - [ ] Running `sudo bash install.sh` on a supported Ubuntu host installs WireGuard, writes `wg0.conf` (address, port, server key, iptables NAT + forwarding on the auto-detected egress interface), enables IP forwarding, and starts `wg-quick@wg0`.
    - [ ] On completion the script prints a summary (server public key, endpoint IP:port) the operator can use to build client configs.
    - [ ] The script is non-interactive by default (suitable for unattended/automation use); it does not block on prompts.
    - [ ] The script fails hard (non-zero exit, clear message) on any critical step rather than leaving a half-configured host.

### 2.2 Optional dashboard install

- **As an operator, I want** the dashboard installed by the same script when I ask for it, **so that** I get the VPN-only web UI without extra steps.
  - **Acceptance Criteria:**
    - [ ] When a dashboard release is specified, the script creates the dashboard system user, directories, sudoers fragment, `clients.json`, `alerts.env`, downloads the release binary, **verifies it against the published `SHA256SUMS`**, installs it, and starts the systemd unit.
    - [ ] When no dashboard release is specified, the script installs only the WireGuard server and skips the dashboard cleanly (no partial dashboard state).
    - [ ] The installed dashboard behaves identically to the EC2-provisioned one (status, throughput, history, geo map, config download, alerting).

### 2.3 Configuration via environment

- **As an operator, I want** to configure the install through environment variables with sensible defaults, **so that** the same script works unattended on a VPS and as EC2 user-data.
  - **Acceptance Criteria:**
    - [ ] Configuration is read from environment variables: server subnet (default `172.16.15.1/24`), listen port (default `51820`), server private key, peers/clients manifest (default empty), dashboard release tag/repo (default unset → dashboard skipped), and the alert knobs/transport secrets.
    - [ ] If the server private key is not provided, the script generates one on the host (`wg genkey`) and persists it under `/etc/wireguard`; if provided, it uses the supplied key.
    - [ ] With no peers configured, the server comes up with zero peers and the operator can add clients afterward (via the dashboard / Terraform / by hand).
    - [ ] The script contains **no Terraform interpolation** — it is plain bash consuming environment variables only.

### 2.4 EC2 path reuses the same script (no behavior change)

- **As the maintainer, I want** the EC2 user-data to call the shared script instead of duplicating the install logic, **so that** the AWS and VPS paths can't drift.
  - **Acceptance Criteria:**
    - [ ] The EC2 user-data is refactored to a thin wrapper that exports the environment from Terraform inputs, runs the shared script, and retains only the AWS-specific concerns (IMDSv2, SSM-sourced key, the S3 `.ready` signal, EIP, awscli).
    - [ ] A `terraform apply` on the refactored module produces a working server + dashboard **equivalent to the pre-refactor box** (regression gate — owner-run; "done" is not claimed without it).
    - [ ] The server private key on EC2 continues to come from SSM (unchanged), and no client private keys are ever generated or stored on the host.

---

## 3. Scope and Boundaries

### In-Scope

- A committed `scripts/install.sh`: one-shot WireGuard server + optional dashboard bootstrap, env-driven, Ubuntu-only, fail-hard.
- Refactor of the EC2 `user-data` to consume the same script via a thin AWS wrapper.
- Download-then-run distribution (review the file, then `sudo bash install.sh`).

### Out-of-Scope (Non-Goals)

- **Client management** — no add/remove/list, no client key generation, no `.conf`/QR output. Preserves the "no client private keys on host" posture.
- **Non-Ubuntu OS support** — no Debian/CentOS/Alpine matrix (unlike angristan).
- **A `curl | bash` one-liner or release-asset distribution** — the script ships in-repo, download-then-run only.
- **CI for the script** — verification is `shellcheck` + a manual VPS dry-run + the EC2 regression apply; no pipeline.
- **Removing AWS specifics from the EC2 path** — IMDSv2, SSM, S3 readiness, and EIP stay in the wrapper, not the shared script.
- **All other roadmap items** (specs 001–013) are separate and out of scope here.
