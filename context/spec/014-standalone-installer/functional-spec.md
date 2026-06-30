# Functional Specification: Standalone Install Script

- **Roadmap Item:** "Standalone installer (Spec D)" (promoting from _Future / Under Consideration_)
- **Status:** Completed _(EC2 path operator-verified 2026-06-29; standalone-only branches code-complete + shellcheck-clean but not VPS-runtime-tested — see annotations)_
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
    - [x] Running `install.sh` on Ubuntu installs WireGuard, writes `wg0.conf` (address, port, server key, iptables NAT + forwarding on the auto-detected egress interface), enables IP forwarding, and starts `wg-quick@wg0`. _(Exercised on EC2 — the wrapper runs this exact script; arm64 box verified 2026-06-29.)_
    - [x] On completion the script prints a summary (server public key, endpoint IP:port). _(`install.sh` summary block; runs on the verified EC2 boot.)_
    - [x] The script is non-interactive by default; it does not block on prompts. _(No prompts; `set -euo pipefail`.)_
    - [x] The script fails hard (non-zero exit, clear message) on any critical step rather than leaving a half-configured host. _(Fail-hard `exit 1` guards throughout; shellcheck-clean.)_

### 2.2 Optional dashboard install

- **As an operator, I want** the dashboard installed by the same script when I ask for it, **so that** I get the VPN-only web UI without extra steps.
  - **Acceptance Criteria:**
    - [x] When a dashboard release is specified, the script creates the dashboard system user, directories, sudoers fragment, `clients.json`, `alerts.env`, downloads the release binary, **verifies it against the published `SHA256SUMS`**, installs it, and starts the systemd unit. _(Exercised on EC2 — dashboard came up with all tabs working, 2026-06-29.)_
    - [x] When no dashboard release is specified, the script installs only the WireGuard server and skips the dashboard cleanly (no partial dashboard state). _(**Code-verified** — block gated on `DASHBOARD_RELEASE_TAG`; **not VPS-runtime-tested** — EC2 always sets the tag.)_
    - [x] The installed dashboard behaves identically to the EC2-provisioned one. _(On EC2 it IS the EC2-provisioned one — same binary/path/unit.)_

### 2.3 Configuration via environment

- **As an operator, I want** to configure the install through environment variables with sensible defaults, **so that** the same script works unattended on a VPS and as EC2 user-data.
  - **Acceptance Criteria:**
    - [x] Configuration is read from environment variables: server subnet, listen port, server private key, peers/clients manifest, dashboard release tag/repo, and alert knobs/transport secrets — with the documented defaults. _(Env contract `install.sh:52-81`; exercised via the EC2 wrapper.)_
    - [x] If the server private key is not provided, the script generates one (`wg genkey`) and persists it under `/etc/wireguard`; if provided, uses the supplied key. _(**Code-verified** `install.sh:126-135`; **not VPS-runtime-tested** — EC2 always supplies the key from SSM, so the generate branch wasn't exercised.)_
    - [x] With no peers configured, the server comes up with zero peers and the operator can add clients afterward. _(**Code-verified** — `WG_PEERS` defaults empty, appended verbatim; **not VPS-runtime-tested** — EC2 supplied peers.)_
    - [x] The script contains **no Terraform interpolation** — plain bash consuming env vars only. _(Verified: runtime `if`/`${VAR:-}`, no `%{ }`/`${ }` template syntax.)_

### 2.4 EC2 path reuses the same script (no behavior change)

- **As the maintainer, I want** the EC2 user-data to call the shared script instead of duplicating the install logic, **so that** the AWS and VPS paths can't drift.
  - **Acceptance Criteria:**
    - [x] The EC2 user-data is refactored to a thin wrapper that exports the environment from Terraform inputs, fetches + SHA256-verifies + runs the shared script, and retains only the AWS-specific concerns (IMDSv2, SSM-sourced key, S3 `.ready`, EIP, awscli). _(Fetch-at-boot wrapper, `user-data.txt`; install logic de-duplicated.)_
    - [x] A `terraform apply` on the refactored module produces a working server + dashboard. _(Owner-verified 2026-06-29 — the arm64 box booted via the fetched `install.sh` (ref `main`, sha `7be62a7…`) with tunnel + all dashboard tabs working.)_
    - [x] The server private key on EC2 continues to come from SSM (unchanged), and no client private keys are ever generated or stored on the host. _(SSM-sourced key path unchanged; no client-key generation anywhere.)_

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
