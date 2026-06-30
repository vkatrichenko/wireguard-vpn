<!--
This document describes HOW to build the feature at an architectural level.
It is NOT a copy-paste implementation guide.
-->

# Technical Specification: Standalone Install Script

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Split today's monolithic `user-data.txt` into two units:

1. **`scripts/install.sh`** (new, repo root) — the portable installer. Pure bash, `set -euo pipefail`, **no Terraform interpolation**, all config from env vars. Owns everything OS-level: apt + WireGuard, arch-detect, server-key handling, `wg0.conf` + NAT/forwarding, `wg-quick@wg0`, and the optional dashboard install (user/sudoers/`clients.json`/`alerts.env`/SHA-verified binary/systemd unit).
2. **`user-data.txt`** (slimmed) — a thin AWS wrapper. Renders env exports from Terraform `${...}` vars, **fetches `install.sh` at boot and runs it as a subprocess**, and keeps only AWS concerns: IMDSv2, SSM-sourced key (already a TF var), awscli, the S3 `.ready` signal, EIP.

**Distribution mechanism — fetch-at-boot, not embed.** Embedding the fully-commented script inline blew past EC2's 16 KB user-data cap (`install.sh` alone is ~17.9 KB). Instead the wrapper **curls `install.sh` from raw GitHub at a pinned ref and verifies it against a Terraform-pinned SHA256** before running it. This keeps user-data tiny regardless of script growth and matches the repo's "explicit, reviewable pin" philosophy (like the AMI / dashboard-tag pins). The script's `$VAR`s are never seen by `templatefile()` at all (the script isn't in the template), so the `$${}` escaping hazard is moot.

## 2. Proposed Solution & Implementation Plan

### 2.1 `scripts/install.sh` — env contract

| Env var | Default | Notes |
|---|---|---|
| `WG_SERVER_NET` | `172.16.15.1/24` | `[Interface] Address` |
| `WG_SERVER_PORT` | `51820` | listen port |
| `WG_SERVER_PRIVATE_KEY` | generate via `wg genkey` | persisted to `/etc/wireguard` |
| `WG_PEERS` | empty | rendered `[Peer]` stanzas |
| `DASHBOARD_PORT` | `8080` | dashboard bind port |
| `DASHBOARD_RELEASE_TAG` / `_REPO` | empty → **skip dashboard** | runtime `if`, replacing the `%{ if … ~}` template gate |
| `CLIENTS_JSON` | empty | dashboard manifest |
| alert knobs + transport secrets | empty | written to `alerts.env` |

**Generalizations required during extraction** (things currently hardcoded/AWS-shaped):

- `LISTEN_ADDR` is hardcoded `172.16.15.1:8080`; derive it as `${WG_SERVER_NET%/*}:${DASHBOARD_PORT}` so a VPS with a different subnet works.
- The dashboard `%{ if … ~}` and `alerts.env` `%{ if … ~}` template conditionals become runtime bash `if [ -n "$VAR" ]`.
- Arch-detect stays in the script (for the dashboard binary). The wrapper does its own minimal `uname -m` for the awscli installer.
- The final "is `wg-quick@wg0` active?" check becomes the script's success gate (non-zero exit on failure); the **S3 signal** built on top of it stays in the wrapper.

### 2.2 `user-data.txt` wrapper

Renders (via `templatefile`): the IMDSv2 block, `export`s of the env contract from TF vars (scalars single-quoted; multi-line `WG_PEERS`/`CLIENTS_JSON` via a quoted heredoc-to-variable so newlines survive and nothing re-expands), then **fetch + verify + run**:

```
mkdir -p /opt/wg-install
curl -fsSL "https://raw.githubusercontent.com/${install_script_repo}/${install_script_ref}/scripts/install.sh" \
  -o /opt/wg-install/install.sh
echo "${install_script_sha256}  /opt/wg-install/install.sh" | sha256sum -c - \
  || { echo "FATAL: install.sh failed checksum"; exit 1; }
bash /opt/wg-install/install.sh || { echo "FATAL: install.sh failed"; exit 1; }
```

…then awscli + the S3 `.ready` loop. Running the fetched script as a subprocess contains its `set -e`; a fetch failure or checksum mismatch aborts the boot (no `.ready`), so a tampered or wrong-ref script never runs.

### 2.3 Module wiring (`terraform/modules/wireguard/`)

- `variables.tf`: add `install_script_repo` (e.g. `vkatrichenko/wireguard-vpn`), `install_script_ref` (a commit SHA or tag pinning the exact `install.sh` version), and `install_script_sha256` (the 64-hex digest of `scripts/install.sh` at that ref; add a `validation` for the hex shape). Pass all three into the `templatefile()` call (replacing the embed).
- The existing `wg_server_*`, `peers`, `clients_json`, `dashboard_*`, alert vars are re-marshalled as env exports inside the wrapper instead of inlined into the install body.
- Dev pins `install_script_ref` + `install_script_sha256` (in `locals.tf` / `main.tf`), updated as an explicit reviewable commit whenever `install.sh` changes (`sha256sum scripts/install.sh`).

## 3. Impact and Risk Analysis

- **EC2 regression (highest).** This rewrites the proven boot path. The rendered user-data must produce a functionally identical box. **Mitigation:** owner `plan` + `apply` + SSH/SSM smoke (WG handshake, dashboard up, `cloud-init-output.log` clean) is a required completion gate; diff the rendered user-data before/after.
- **User-data 16 KB limit — drove the fetch-at-boot decision.** Embedding the fully-commented `install.sh` (~17.9 KB) rendered ~23 KB, over EC2's 16 KB cap. Fetch-at-boot keeps user-data ~5 KB regardless of script growth; the size risk is retired.
- **Repo must be public.** The raw-GitHub fetch (like the dashboard binary already) requires the repo to be public — a 404 on a private repo aborts the boot. Inherits the same prerequisite as the dashboard release; not a new constraint.
- **Pin sequencing.** `install_script_ref` + `install_script_sha256` can't be finalized until `install.sh` exists at a public commit/ref. Same explicit-pin ordering as the AMI / `dashboard_release_tag`: push `install.sh`, then pin the ref + `sha256sum` in Terraform before apply. A TODO at the pin should flag this.
- **Supply chain.** The fetched script is verified against the Terraform-pinned SHA256 before it runs as root; a mismatch fails the boot. The pin is a reviewable commit, so changing what runs at boot is an auditable diff.
- **Secrets in env.** The wrapper exports the server key + alert secrets into the script's environment (root-only `/proc/<pid>/environ`) — same trust level as today's in-line substitution; they still land only in root-owned `0640` files. No new exposure.
- **PR #41 overlap.** Resolved — #41 is merged; 014 is being built on the merged base (branch `feat/spec-014`).

## 4. Testing Strategy

- `shellcheck scripts/install.sh` — now possible (real bash, not a template); wire it into `make pre-commit` if cheap.
- **VPS dry-run:** run on a throwaway Ubuntu host (multipass/container/cheap VPS) with env set — confirm WG handshake + dashboard reachable on the tunnel IP. Also run with `DASHBOARD_RELEASE_TAG` empty to confirm the WG-only path.
- **EC2 regression (owner-run):** `plan` (expect user-data change → instance replacement), `apply`, smoke-test the box. Not claimable in-session.
