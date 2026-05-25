# Deployment Options for wireguard-vpn

**Status:** Draft for review — captures the brainstorm sessions of 2026-05-14 and 2026-05-15.

**Decisions recorded during the brainstorm:**

- **WG integration model:** *manage-only*. Kernel WireGuard runs on the host; the dashboard binary manages config and the systemd unit. No embedded `wireguard-go`. Bundling the tunnel itself is explicitly rejected — kernel WG is faster, better-supported, and already present on every modern Linux distro.
- **Architecture target:** *Archetype 2 — portable single-binary distro*. The binary becomes the product. Terraform is one of several deployment paths, not the headline.
- **Client source of truth:** *Terraform*, in both deployment modes. The HCL `clients_config` list (current shape: `{ name, address, public_key }`) declares clients. No imperative client management from the dashboard UI.
- **Client transport:** *provider-flagged* on the dashboard. `aws` mode reads from SSM Parameter Store; `file` mode reads from `/etc/wireguard-dashboard/clients.yaml`. The dashboard does not care how the source is populated — it just reads.
- **UI model:** *read-only for clients*. The dashboard surfaces a single **"Refresh & Apply"** control with a preview-diff intermediate step. No Add/Edit/Delete forms, no server-side keygen, no QR generation.
- **No bootstrap mode.** Dashboard binds to the WG tunnel IP from day one. The first client is declared externally before the dashboard ever needs to be reachable.
- **No admin password.** VPN membership is the access gate (per the v3 spec for the existing `002-web-dashboard` slice). Defense-in-depth via the read-only model.
- **Reconcile mechanism:** `wg syncconf` for all peer changes — zero downtime for unrelated peers. `wg-quick down/up` is reserved for `[Interface]` section changes and requires explicit operator confirmation.

This document is the option-space writeup; it is not yet an implementation plan. Open questions are listed in [§6](#6-open-questions--decisions-deferred).

---

## 1. Why revisit deployment

### 1.1 Today's shape

- WireGuard runs on a single EC2 instance, provisioned by Terraform under `terraform/dev/`.
- WG itself is installed at first boot via cloud-init user-data (`terraform/modules/wireguard/templates/user-data.txt`).
- The dashboard is a separate Go static binary (`dashboard/`), built by a GitHub Actions workflow, uploaded to S3, and deployed onto the EC2 over `ssm:SendCommand`.
- Peers are declared in Terraform's `clients_config` list and rendered into `/etc/wireguard-dashboard/clients.json` plus the WG peers section of `wg0.conf` by user-data — *one-shot at first boot*, so adding a client today requires an instance replacement.

### 1.2 Friction with the current shape

- **AWS-locked.** "Jordan the Privacy-First Developer" (per `context/product/product-definition.md`) wants a $5 Hetzner box, not an AWS account.
- **CI is mandatory.** The binary lives in S3; without GitHub Actions + AWS OIDC + an SSM document, there is no way to ship a new version.
- **Provisioning is one-shot.** Cloud-init only runs at first boot. Any peer change requires an instance replacement — operationally painful.
- **Two sources of truth latent.** Terraform declares peers; the dashboard reads a derived file. Today this is fine because both are immutable post-boot, but the moment anyone wants to mutate clients without a Terraform apply, the tension shows up.

### 1.3 Drivers for change (priority order)

1. **Portability.** Run on any Linux VPS *or* on EC2, with the same artifact.
2. **Lower the deployment barrier.** No AWS account, no CI required to stand up a working server.
3. **No-downtime peer changes.** Adding or removing a client should not bounce the EC2 instance and should not interrupt other active peers.
4. **Keep IaC as the management plane.** Terraform stays the source of truth — operators who already think declaratively should not have to learn a UI for clients.

---

## 2. Architecture archetypes

Three coarse shapes for where this could go.

### Archetype 1 — Status quo, polished

Keep the AWS + Terraform + cloud-init + S3/SSM pipeline. Replace one-shot peer rendering at boot with **SSM-driven runtime reconciliation**: Terraform writes the client list to SSM, the dashboard pulls and applies via `wg syncconf` without restarting WireGuard. No instance replacement required to add a client.

| Aspect | Detail |
|--------|--------|
| Host | EC2 (Amazon Linux 2023) |
| WG install | Cloud-init user-data |
| Binary deploy | GitHub Actions → S3 → SSM SendCommand |
| Client transport | TF → SSM `/config/wireguard/clients` → dashboard pull |
| Peer reconcile | `wg syncconf` triggered by dashboard "Refresh & Apply" |

**Pros:** preserves IaC for AWS users; resolves the no-downtime requirement on the existing platform; minimal architectural delta from today.

**Cons:** still AWS-only; mandatory CI for binary updates; doesn't help the "Jordan" persona.

### Archetype 2 — Portable single-binary distro **(recommended)**

The binary becomes the product. It gains `install`, `uninstall`, and `update` subcommands that bootstrap and manage a working WG server on any Linux host. Terraform stays the source of truth for clients (same `clients_config` shape) but the *transport* between TF and the host swaps based on a provider flag: SSM on AWS, a file on a VPS.

| Aspect | Detail |
|--------|--------|
| Host | Any Linux with kernel ≥ 5.6 (Ubuntu, Debian, AL2023, Fedora; Alpine/Arch stretch) |
| WG install | Binary `install` subcommand calls the host's package manager |
| Binary deploy | GitHub Releases + checksum + signature; `update` subcommand for in-place |
| Client transport | Provider flag: `aws` → SSM pull, `file` → local file read |
| Peer reconcile | `wg syncconf` triggered by dashboard "Refresh & Apply" |

**Pros:** hosting-agnostic; no CI required for end users (GitHub Releases is enough); preserves IaC; same reconcile mechanism on every host; fits both personas.

**Cons:** installer must handle distro variation (apt/dnf/apk/pacman, ufw/firewalld/nftables); broader test surface.

### Archetype 3 — Container distro

A single OCI image with the binary plus `wireguard-tools` baked in. Tunnel via host kernel WG using `--cap-add NET_ADMIN --net host`. Runs anywhere Docker/Podman runs.

| Aspect | Detail |
|--------|--------|
| Host | Anywhere Docker/Podman runs (still needs host kernel WG module) |
| WG install | `wireguard-tools` baked into image; kernel module from host |
| Binary deploy | `docker pull` / `podman pull` |
| Client transport | Same provider flag; volume-mounted file or SSM |
| Peer reconcile | `wg syncconf` (same as other archetypes) |

**Pros:** uniform deploy regardless of host distro; trivial upgrade path; rollback is `docker run` with the previous tag.

**Cons:** `--net host` + `NET_ADMIN` is a privileged container — security-conscious users will balk; kernel module still must exist on host (no isolation benefit).

---

## 3. Cross-cutting axes

The archetypes above are bundles of choices across the following axes.

### 3.1 Host

| Option | Today | Archetype 2 target |
|--------|-------|--------------------|
| EC2 via Terraform | primary | first-class |
| Bare VPS (Hetzner / DO / Vultr / Linode) | unsupported | first-class |
| Container on any host | unsupported | unsupported (Archetype 3 only) |
| Raspberry Pi / home-lab | unsupported | supported once `linux/arm64` build lands |

### 3.2 WG installation

| Option | Pros | Cons |
|--------|------|------|
| Cloud-init user-data (current) | One-shot, ties install to instance lifecycle | EC2-only; re-runs require instance replacement |
| Installer script bundled separately | Portable; easy to read/audit | Two artifacts (binary + script); drift between them |
| Binary self-install (`./wireguard-dashboard install`) | Single artifact; idempotent re-runs; can detect distro | Requires the binary to embed distro/package-manager logic |
| Pre-baked AMI (Packer) | Fast boot; immutable | AWS-only; build pipeline; doesn't help VPS users |

Archetype 2 picks **binary self-install**.

### 3.3 Binary distribution

| Option | Pros | Cons |
|--------|------|------|
| GitHub Actions → S3 → SSM (current) | Works for the existing EC2 | AWS-coupled; private to repo owner; not consumable by external users |
| GitHub Releases | Public, universal, free | Requires checksum / signature discipline |
| Container registry (GHCR) | Built-in versioning + tags | Only useful for Archetype 3 |
| Distro packages (.deb / .rpm) | Native uninstall, dependency declaration | High maintenance; per-distro repo hosting |

Archetype 2 picks **GitHub Releases** as primary, with optional `.deb` / `.rpm` later.

### 3.4 Client transport

Terraform is the source of truth in both modes — but the path from `terraform apply` to the host's running kernel WG state differs:

| Mode | TF writes to | Dashboard reads from | Reconcile trigger |
|------|--------------|----------------------|-------------------|
| `aws` | SSM parameter `/config/wireguard/clients` (single JSON/YAML blob) | SSM `GetParameter` on Refresh | Operator clicks "Refresh & Apply" |
| `file` | A local file the operator (or TF `provisioner "file"`, ansible, scp) pushes onto the host at `/etc/wireguard-dashboard/clients.yaml` | Direct file read on Refresh | Operator clicks "Refresh & Apply" |

The dashboard surfaces a `ClientSource` Go interface; the rest of the binary is identical between modes.

---

## 4. Archetype 2 deep dive

What it actually takes to ship the portable single-binary distro.

### 4.1 Binary contents

The dashboard binary stays a single static Go binary (`CGO_ENABLED=0` — the current `dashboard/Makefile` already does this). What changes:

- Cross-compile both `linux/amd64` and `linux/arm64` (covers Hetzner ARM, AWS Graviton, Raspberry Pi 4/5).
- Embed templates, htmx, Chart.js, sudoers snippet, systemd unit, and the default config via `embed.FS` (most of this is already in place — see `dashboard/embed.go`).
- New subcommands:
  - `install` / `uninstall` / `update` — host lifecycle.
  - `serve` (default) — what runs under systemd.
  - `reconcile` — non-interactive trigger for the same reconcile the UI button runs (useful from cron, SSM SendCommand, or TF `local-exec`).

**WireGuard itself is not bundled.** Manage-only means the binary calls `wg`, `wg-quick`, and `wg syncconf` against the host's kernel implementation. The kernel module + `wireguard-tools` package are *runtime dependencies*, installed for the operator by the `install` subcommand.

### 4.2 The `install` subcommand

Idempotent, re-runnable, root-required. Significantly simpler than the original sketch because there's no bootstrap mode and no admin password to set up.

| # | Step | Detail |
|---|------|--------|
| 1 | **Preflight** | Must be root; Linux only; kernel ≥ 5.6; `/dev/net/tun` present; no existing `wg0` (refuses unless `--force`) |
| 2 | **Detect distro** | Parse `/etc/os-release`, pick `apt-get` / `dnf` / `apk` / `pacman`; fail with a clear message if unknown |
| 3 | **Interactive prompts (or flags)** | `--provider=aws\|file`, public endpoint (auto-detected via STUN/IMDSv2, confirm/override), WG CIDR (default `172.16.15.0/24`), external interface (auto-detected from `ip route get 1.1.1.1`), listen port (default `51820`), client DNS (default `1.1.1.1, 9.9.9.9`). `--yes` + `--config install.yaml` for unattended installs |
| 4 | **Install wireguard-tools** | `apt-get install -y wireguard-tools` (or equivalent); skip if already present |
| 5 | **Verify kernel module** | `modprobe wireguard` + check `/sys/module/wireguard`; fail loudly with remediation |
| 6 | **Enable IP forwarding** | `sysctl -w net.ipv4.ip_forward=1` + persist to `/etc/sysctl.d/99-wireguard.conf` |
| 7 | **Configure firewall** | Detect `ufw` / `firewalld` / `nftables` / `iptables-direct`; open UDP `<port>` on the external interface |
| 8 | **Generate server keypair** | If `/etc/wireguard/server.key` absent: `wg genkey | tee server.key | wg pubkey > server.pub`; chmod 0600 |
| 9 | **Render `/etc/wireguard/wg0.conf`** | `[Interface]` only — private key, listen port, address `172.16.15.1/24`, NAT `PostUp` / `PostDown` rules on the detected external interface. Peers section initially empty |
| 10 | **Create system user** | `useradd --system --no-create-home --shell /usr/sbin/nologin wireguard-dashboard` + make `/var/lib/wireguard-dashboard` and `/etc/wireguard-dashboard` |
| 11 | **Write `/etc/wireguard-dashboard/config.yaml`** | Includes the `client_source` block with the chosen provider |
| 12 | **Drop sudoers snippet** | `/etc/sudoers.d/wireguard-dashboard` (mode 0440) — NOPASSWD for `wg show *`, `wg syncconf wg0 *`, `systemctl restart wg-quick@wg0`, `systemctl status wg-quick@wg0` |
| 13 | **Install binary** | Copy self to `/opt/wireguard-dashboard/bin/wireguard-dashboard`, chmod 0755 |
| 14 | **Drop systemd units** | `/etc/systemd/system/wireguard-dashboard.service` (`After=wg-quick@wg0.service`, `User=wireguard-dashboard`, `Restart=on-failure`, `Environment=LISTEN_ADDR=172.16.15.1:8080`). `systemctl daemon-reload` |
| 15 | **Initial client fetch** | Run one `reconcile` pass — fetches from the configured source (SSM or file) and renders the peers section before WG comes up. If no clients yet, leaves it empty and warns the operator |
| 16 | **Start services** | `systemctl enable --now wg-quick@wg0` then `systemctl enable --now wireguard-dashboard` |
| 17 | **Print summary** | Dashboard URL (`http://172.16.15.1:8080`, reachable only over the VPN), next-steps reminder ("declare your first client in Terraform and run `terraform apply`") |

### 4.3 The `uninstall` subcommand

Symmetric. Flags: `--keep-data` (preserve `/var/lib/wireguard-dashboard` and `/etc/wireguard-dashboard`), `--purge` (remove everything including server keys). Always interactive confirm unless `--yes`.

### 4.4 The `update` subcommand

Downloads the new binary from GitHub Releases over HTTPS, verifies SHA256 against the release's checksum file, verifies signature (minisign or cosign), atomic swap (`rename(2)`), `systemctl restart wireguard-dashboard`. Pins to the release tag the operator chose; no auto-upgrade.

### 4.5 Configuration & state layout

| Path | Purpose | Owner |
|------|---------|-------|
| `/etc/wireguard/wg0.conf` | Kernel WG config — server section + peers section rendered from the fetched client list | Dashboard rewrites; kernel reads |
| `/etc/wireguard/server.{key,pub}` | Server keypair | Generated by `install`; never overwritten |
| `/etc/wireguard-dashboard/config.yaml` | Listen address, WG CIDR, external interface, public endpoint, client DNS, `client_source` provider config | Operator-edited |
| `/etc/wireguard-dashboard/clients.yaml` | Local client list — *primary source* in `file` mode; *cache* in `aws` mode (offline fallback view) | `file` mode: operator/TF; `aws` mode: dashboard writes after successful SSM fetch |
| `/var/lib/wireguard-dashboard/metrics.db` | SQLite metrics + handshake events + reconcile audit log | Dashboard |
| `/etc/sudoers.d/wireguard-dashboard` | NOPASSWD sudoers snippet | `install` writes; operator should not edit |

Example `config.yaml`:

```yaml
listen_addr: 172.16.15.1:8080
wg_cidr: 172.16.15.0/24
external_interface: eth0
public_endpoint: 1.2.3.4
client_dns: [1.1.1.1, 9.9.9.9]

client_source:
  provider: aws            # aws | file
  aws:
    ssm_parameter: /config/wireguard/clients
    region: us-east-1
  file:
    path: /etc/wireguard-dashboard/clients.yaml
```

Backup recipe: `tar -czf backup.tgz /etc/wireguard /etc/wireguard-dashboard /var/lib/wireguard-dashboard`. Restore: untar and `systemctl restart wireguard-dashboard wg-quick@wg0`.

### 4.6 Distribution & releases

- **GitHub Releases** hosts the cross-compiled binaries (`wireguard-dashboard_linux_amd64`, `wireguard-dashboard_linux_arm64`), a `SHA256SUMS` file, and a `SHA256SUMS.sig` (minisign).
- **Public install one-liner** for the README:
  ```
  curl -sSL https://github.com/<org>/wireguard-dashboard/releases/latest/download/install.sh | sudo bash
  ```
  The script downloads the right binary for the architecture, verifies the checksum, then runs `wireguard-dashboard install`.
- **Goreleaser** for the release pipeline — builds the matrix, generates the checksum file, signs, drafts the GH release.
- **Existing S3+SSM pipeline becomes Archetype-1-only.** Kept for AWS users who want the IaC path, but not required.

### 4.7 Distro test matrix

Honest scope warning: the installer's distro matrix is where this lives or dies.

| Tier | Distros | Reasoning |
|------|---------|-----------|
| **Tier 1 (must work for v1)** | Ubuntu 22.04, Ubuntu 24.04, Debian 12, Amazon Linux 2023 | The 90% of VPS + EC2 surface |
| **Tier 2 (best-effort v1)** | Fedora 40, Rocky/Alma 9 | Common but firewalld zoning adds work |
| **Tier 3 (post-v1)** | Alpine, Arch | OpenRC and rolling release — cut from v1 |

For each Tier 1 distro: fresh VM, `install`, declare a client, reconcile, connect, `uninstall`, confirm no residue.

### 4.8 Client source providers

The dashboard exposes a small Go interface:

```go
type ClientSource interface {
    Fetch(ctx context.Context) ([]Client, error)
    Name() string
}
```

Two implementations, selected via `client_source.provider` in `config.yaml`:

| Provider | Implementation | TF writes to it via |
|----------|----------------|--------------------|
| `aws` | `awsSSMSource` — `ssm:GetParameter` on the configured path; expects a JSON or YAML blob containing the client list | `aws_ssm_parameter` resource — value is `jsonencode(local.clients_config)` |
| `file` | `fileSource` — reads the configured file path | TF renders a `local_file` resource, then either `provisioner "file"` over SSH, an ansible step, or operator `scp` puts it on the host |

The rest of the dashboard (validator, renderer, reconciler) takes a `[]Client` and doesn't care which source produced it. Switching modes is a config-only change after install.

**SSM size decision.** SSM Standard params cap at 4 KB; Advanced params at 8 KB ($0.05/month each). A JSON blob of 50 clients (`{name, address, public_key, created_at}`) is ~7 KB — borderline. Start with **a single Advanced param**; migrate to a hierarchy (`/config/wireguard/clients/<name>` with `GetParametersByPath`) only if the client list grows past ~50. Tracked in [§6](#6-open-questions--decisions-deferred).

---

## 5. Client management — Terraform as source of truth

The dashboard never mutates the client list. It reads, validates, renders, and reconciles. All adds/removes/edits happen in Terraform.

### 5.1 The two transport paths

**AWS path:**

1. Operator edits `terraform/dev/main.tf` — appends or removes an entry in `clients_config`.
2. `terraform plan -out=tfplan` — shows `aws_ssm_parameter.clients` value will change.
3. `terraform apply tfplan` — SSM parameter updated. ~5 s. **No effect on the running VPN.**
4. Operator opens the dashboard (over the VPN), clicks **Refresh & Apply**.
5. Dashboard runs the reconcile flow (§5.3) — peer added/removed via `wg syncconf`, no interruption to unrelated peers.

**VPS path:**

1. Operator edits `terraform/<vps>/main.tf` — same `clients_config` shape.
2. TF renders a `local_file` with the YAML.
3. The file gets onto the host — choice of mechanism (`provisioner "file"` over SSH from TF, ansible step, manual `scp`). The choice is decoupled from the dashboard's reconcile logic.
4. Operator opens the dashboard, clicks **Refresh & Apply**.
5. Same reconcile flow.

### 5.2 The Refresh & Apply UX

Two-click flow:

1. **Click "Refresh"** — dashboard fetches from the configured source (SSM or file), parses, validates, computes the diff vs current kernel state, and renders a preview:
   > **2 peers will be added:** `alice` (172.16.15.6/32), `elena` (172.16.15.7/32)
   > **1 peer will be removed:** `bob` (172.16.15.4/32) — *active session, will be dropped*
   > **Existing peers unchanged:** `carol`, `dave` (sessions preserved)
2. **Click "Apply"** — the actual reconcile runs (steps 4–10 of §5.3). The preview gates destructive changes (especially peer removal) so they're visible before they happen.

For non-interactive use (cron, SSM SendCommand, TF `local-exec`), the `wireguard-dashboard reconcile` CLI subcommand bypasses the preview and applies directly. Flag `--dry-run` prints the diff without applying.

### 5.3 Transactional reconcile flow

| # | Step | Failure handling |
|---|------|------------------|
| 1 | Acquire in-process mutex (serializes concurrent clicks) | If contended: "another reconcile in progress, retry" |
| 2 | `source.Fetch()` — SSM `GetParameter` or file read | Network / file error → show in UI, leave runtime untouched |
| 3 | Parse + validate (unique names, unique IPs in CIDR, valid base64 pubkeys, IP fits configured `WG_CIDR`) | Validation error → show inline diff with problem highlighted; no kernel changes |
| 4 | Detect `[Interface]` section drift (port, CIDR, server key) vs running `wg show wg0` | If detected → require explicit second confirmation; flag as a downtime operation (§5.4) |
| 5 | Render proposed config to `/etc/wireguard/wg0.conf.new` (temp file) | Disk error → bail |
| 6 | `wg-quick strip /etc/wireguard/wg0.conf.new > /tmp/wg0.synced.<pid>` | Bail on error |
| 7 | `sudo wg syncconf wg0 /tmp/wg0.synced.<pid>` | Non-zero exit → leave old `wg0.conf` in place, show kernel error in UI |
| 8 | Atomic rename: `mv /etc/wireguard/wg0.conf.new /etc/wireguard/wg0.conf` | `rename(2)` is atomic; either-or |
| 9 | Update local cache: write fetched list to `/etc/wireguard-dashboard/clients.yaml` (even in `aws` mode — it's the offline fallback view) | Non-critical; log warning |
| 10 | Record audit event in `metrics.db` (who reconciled, what diff, timestamp) | Non-critical |
| 11 | UI toast: "+1 added (`alice`), −1 removed (`bob`). Existing sessions kept." | — |

If anything between steps 5–7 fails, the kernel state and `wg0.conf` on disk remain in the *old* state. The operator reads the error and retries after fixing the source.

### 5.4 Downtime characteristics by change type

| Change | Existing peers' impact | Mechanism |
|--------|------------------------|-----------|
| **Add peer** | None — they don't know it happened | `wg syncconf` (peer added) |
| **Remove peer** | Only the removed peer drops; others untouched | `wg syncconf` (peer removed) |
| **Rotate peer's pubkey** | Only that peer must re-handshake with new key; others untouched | `wg syncconf` (peer's pubkey replaced) |
| **Change peer's AllowedIPs** | Only that peer is updated; session continues | `wg syncconf` (field updated in place) |
| **Change server's listen port** | All peers drop (interface restart required) | `wg-quick down/up` — warn loudly in UI, require typed confirmation |
| **Change server's private key** | All peers drop | `wg-quick down/up` — warn loudly, require typed confirmation |
| **Change server's CIDR** | All peers drop | `wg-quick down/up` — warn loudly, require typed confirmation |

The first four rows are the common case and are **zero-downtime for unrelated peers** — that is the whole point of the design. The bottom three rows are rare and the dashboard surfaces them as exceptional events.

### 5.5 Adding a client end-to-end (AWS example)

1. Operator generates a keypair locally:
   ```bash
   wg genkey | tee alice.key | wg pubkey > alice.pub
   ```
2. Edits `terraform/dev/main.tf`:
   ```hcl
   clients_config = [
     # ... existing ...
     { name = "alice", address = "172.16.15.6/32", public_key = "<contents of alice.pub>" },
   ]
   ```
3. `terraform plan -out=tfplan && terraform apply tfplan` — SSM parameter updated. **No effect on the running VPN.** Bob, Carol, Dave keep their sessions.
4. Operator (already on VPN) opens `http://172.16.15.1:8080`, clicks **Refresh**.
5. Preview shows: *+1 alice, others unchanged*.
6. Operator clicks **Apply**. Reconcile runs in < 1 second. `alice` is now live.
7. Operator hands Alice the `.conf` (constructed locally — `[Interface]` with `alice.key` + `[Peer]` with server's pubkey and endpoint). Alice imports it, `wg-quick up`, connects.

Total wall-clock time: ~30 seconds, zero downtime.

---

## 6. Open questions / decisions deferred

These don't block writing the doc, but each blocks implementation:

1. **Endpoint detection on bare VPS.** EC2 has IMDSv2; Hetzner / DO don't expose a public IP via metadata in the same shape. STUN against a public server? Read the route and ask the operator? Prompt-with-detected-default is probably the right UX.
2. **Public endpoint hostname vs. IP.** Operators with a domain might want `vpn.example.com`. Dashboard should accept either and not try to resolve at install time.
3. **VPS file-push mechanism.** Three viable options: TF `provisioner "file"` over SSH, ansible, operator `scp`. Pick a default for the docs but support all three.
4. **SSM single-param vs. hierarchy.** Default to single Advanced param. Define the threshold (~50 clients?) at which we migrate to a hierarchy, and document the migration.
5. **Preview-before-apply UX details.** Diff format — table, side-by-side, unified? Probably a small table is enough.
6. **Reconcile authorization.** Anyone on the VPN can click "Apply." Is that fine, or do we need a separate operator role? The v3 spec already accepted VPN-membership as the access gate, so probably fine — but worth flagging.
7. **`linux/arm64` test coverage.** Need a CI runner with ARM (GitHub-hosted ARM runners are now GA; Hetzner CAX11 is the obvious target host).
8. **Distro detection edge cases.** Minimal Debian without `iproute2`? Fail at preflight with a clear remediation hint.
9. **Multi-server topology.** Out of scope for v1, but should the data model preclude it? Probably not — `config.yaml` listing one server is fine for now, expandable later.
10. **Container archetype later?** Once Archetype 2 is solid, an Archetype 3 image is mostly a Dockerfile wrapping the same binary. Defer the decision; don't preclude it.
11. **Open-source repo restructure.** Today the repo is `wireguard-vpn` and Terraform is the headline. If Archetype 2 lands, the binary is the headline and `terraform/` becomes one of several deployment recipes (`deploy/aws-terraform/`, `deploy/vps-installer/`, `deploy/docker/`).

---

## 7. Recommended path forward

1. **Land SSM-driven client transport on top of current Archetype 1.** Build the `awsSSMSource`, the reconcile flow, and the Refresh & Apply UI on the existing EC2 deployment. Switch the Terraform module to render clients to SSM instead of into user-data. **This alone eliminates the "instance replacement on every peer change" pain** and validates the reconcile mechanism on familiar infrastructure.
2. **Add `install` / `uninstall` / `update` subcommands** targeting Tier 1 distros. End-to-end-test against fresh Ubuntu 24.04 and Hetzner CAX11 (or equivalent) before claiming v1.
3. **Add `fileSource` provider.** Same reconcile flow, file-based transport. Validate the VPS path.
4. **Cut the first GitHub Release** with cross-compiled binaries + checksum + signature.
5. **Promote the install one-liner** in the README as the primary path; relegate the AWS+Terraform path to a `deploy/aws/` subdir with its own README.
6. **(Optional) Container image** after Archetype 2 is stable — small Dockerfile wrapping the same binary.
