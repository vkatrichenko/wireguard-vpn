<!--
This document describes HOW to build the feature at an architectural level.
It is NOT a copy-paste implementation guide.
-->

# Technical Specification: Standalone Install Script

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Split today's monolithic `user-data.txt` into two units:

1. **`scripts/install.sh`** (new, repo root) — the portable installer. Pure bash, `set -euo pipefail`, **no Terraform interpolation**, all config from env vars. Owns everything OS-level: apt + WireGuard, arch-detect, server-key handling, `wg0.conf` + NAT/forwarding, `wg-quick@wg0`, and the optional dashboard install (user/sudoers/`clients.json`/`alerts.env`/SHA-verified binary/systemd unit).
2. **`user-data.txt`** (slimmed) — a thin AWS wrapper. Renders env exports from Terraform `${...}` vars, embeds + runs the script as a **subprocess**, and keeps only AWS concerns: IMDSv2, SSM-sourced key (already a TF var), awscli, the S3 `.ready` signal, EIP.

The module embeds the script by reading it with `file()` and passing it as a **`templatefile()` variable** — so the script's own `$VAR`s are never re-interpolated — sidestepping the `$${}` escaping hazard entirely.

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

Renders (via `templatefile`): the IMDSv2 block, `export`s of the env contract from TF vars (scalars single-quoted; multi-line `WG_PEERS`/`CLIENTS_JSON` via a quoted heredoc-to-variable so newlines survive and nothing re-expands), then:

```
cat > /opt/wg-install/install.sh <<'INSTALL_EOF'
${install_script}
INSTALL_EOF
bash /opt/wg-install/install.sh || { echo "FATAL: install.sh failed"; exit 1; }
```

…then awscli + the S3 `.ready` loop. The `'INSTALL_EOF'` quoting stops bash expanding the script body; `${install_script}` is a templatefile _variable_ (file content), so its `$VAR`s aren't interpolated. Running as a subprocess contains the script's `set -e`.

### 2.3 Module wiring (`terraform/modules/wireguard/`)

- `locals.tf`: add `install_script = file("${path.module}/../../../scripts/install.sh")` to the existing `templatefile()` call's variable map.
- No new variables; the existing `wg_server_*`, `peers`, `clients_json`, `dashboard_*`, alert vars are simply re-marshalled as env exports inside the wrapper instead of inlined into the install body.

## 3. Impact and Risk Analysis

- **EC2 regression (highest).** This rewrites the proven boot path. The rendered user-data must produce a functionally identical box. **Mitigation:** owner `plan` + `apply` + SSH/SSM smoke (WG handshake, dashboard up, `cloud-init-output.log` clean) is a required completion gate; diff the rendered user-data before/after.
- **User-data 16 KB limit.** Embedding the script keeps total size ≈ today's (content relocated, not added), but it must be re-checked after the refactor; if exceeded, fall back to fetch-at-boot from a pinned tag.
- **Secrets in env.** The wrapper exports the server key + alert secrets into the script's environment (root-only `/proc/<pid>/environ`) — same trust level as today's in-line substitution; they still land only in root-owned `0640` files. No new exposure.
- **PR #41 overlap.** Spec 013 already edits this same `user-data.txt` and is open in PR #41 — land #41 first, then build 014 on the merged base to avoid a conflicting rewrite.

## 4. Testing Strategy

- `shellcheck scripts/install.sh` — now possible (real bash, not a template); wire it into `make pre-commit` if cheap.
- **VPS dry-run:** run on a throwaway Ubuntu host (multipass/container/cheap VPS) with env set — confirm WG handshake + dashboard reachable on the tunnel IP. Also run with `DASHBOARD_RELEASE_TAG` empty to confirm the WG-only path.
- **EC2 regression (owner-run):** `plan` (expect user-data change → instance replacement), `apply`, smoke-test the box. Not claimable in-session.
