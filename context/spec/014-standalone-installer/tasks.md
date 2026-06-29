# Tasks: Standalone Install Script

> **Sequencing:** Build on top of merged PR #41 — spec 013 is still rewriting `user-data.txt`. Land #41 first; Slice 3 here assumes the merged user-data as its base. Each slice leaves the repo in a coherent, runnable state (the VPS installer is fully usable after Slice 2, before the riskier EC2 refactor).

---

### Slice 1 — `scripts/install.sh`: WireGuard server (standalone, no dashboard)

- [x] Create `scripts/install.sh` (`set -euo pipefail`, usage header comment): env contract (`WG_SERVER_NET`/`_PORT`/`_PRIVATE_KEY`/`WG_PEERS` with defaults), apt + WireGuard install, arch-detect, server-key generate-if-unset (persist to `/etc/wireguard`), `wg0.conf` with NAT + forwarding on the auto-detected egress iface, `ip_forward`, enable/start `wg-quick@wg0`, fail-hard success gate. **[Agent: linux-cloud-init]**
- [x] Verify: `shellcheck scripts/install.sh` clean ✓ (via `koalaman/shellcheck` Docker, exit 0). Boot/handshake dry-run on a real Ubuntu host is **owner-run** (system-mutating; can't run on the macOS dev host). **[Agent: linux-cloud-init]**

### Slice 2 — Add the dashboard install to `install.sh`

- [x] Extend the script with the dashboard block gated on `DASHBOARD_RELEASE_TAG` (runtime `if`, replacing the template `%{ if }`): dashboard user/dirs/sudoers, `clients.json` + `alerts.env` from env (runtime conditionals for optional knobs/secrets), SHA-verified arch binary download + install, systemd unit with `LISTEN_ADDR` derived from `${WG_SERVER_NET%/*}:${DASHBOARD_PORT}`. **[Agent: linux-cloud-init]**
- [x] Verify: `shellcheck` clean ✓ (Docker, exit 0); all `DASHBOARD_*` vars `${VAR:-}`-defaulted (set -u safe). With-tag / no-tag boot dry-run is **owner-run** on a real Ubuntu host. **[Agent: linux-cloud-init]**

_At the end of Slice 2 the VPS use case is fully delivered; EC2 still uses its own user-data (logic duplicated, temporarily)._

### Slice 3 — Refactor EC2 user-data to consume the shared script

- [ ] Add `install_script = file("${path.module}/../../../scripts/install.sh")` to the `templatefile()` var map in `terraform/modules/wireguard/locals.tf`. **[Agent: terraform-aws]**
- [ ] Slim `user-data.txt` to the AWS wrapper: IMDSv2, `export` the env contract from TF vars (scalars single-quoted; `WG_PEERS`/`CLIENTS_JSON` via quoted heredoc-to-var), embed+run the script as a subprocess with exit-code check, then awscli + S3 `.ready` loop + EIP. Remove the now-duplicated install logic. **[Agent: linux-cloud-init]**
- [ ] Verify (agent): `terraform fmt -recursive`; `make pre-commit`; render & diff the user-data vs the pre-refactor version; confirm rendered size < 16 KB. **[Agent: terraform-aws]**
- [ ] **Owner-run:** `terraform plan -out=tfplan` (expect a user-data change → instance replacement), `apply`, then SSM/SSH smoke — WG handshake, dashboard up, `cloud-init-output.log` clean. Required regression gate. **(owner)**

### Slice 4 — Optional: wire `shellcheck` for `scripts/*.sh`

- [x] **Mechanism changed → standalone `make shellcheck` target** (not a pre-commit hook). A pre-commit hook proved infeasible: the pinned `ghcr.io/antonbabenko/pre-commit-terraform:v1.105.0` image bundles no `shellcheck` binary, and `make pre-commit` mounts no docker socket (so the `language: docker_image` hook can't run). Added a `shellcheck` target pinned to `koalaman/shellcheck:v0.11.0` instead; `make shellcheck` lints `scripts/*.sh` (exit 0), `make pre-commit`'s four terraform hooks unaffected. **[Agent: devsecops-quality]**

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slices 1–2 verify | No throwaway Ubuntu VPS in-session; systemd-in-container is unreliable | shellcheck runs in-session; full boot/handshake dry-run is owner-run (or a real cheap VPS) |
| Slice 3 owner-run | `plan`/`apply` are owner-only (CLAUDE.md); this is the EC2-regression gate | Agent does fmt/validate/render-diff/size; owner runs plan + apply + smoke |
| Whole spec | Rewrites `user-data.txt` also changed by open PR #41 | Land #41 first; implement 014 on the merged base |
