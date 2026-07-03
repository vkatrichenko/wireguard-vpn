# WireGuard VPN — on AWS *or any Ubuntu VPS* — with a built-in observability dashboard

> A fully codified, self-hosted [WireGuard](https://www.wireguard.com/) VPN server — provision it end-to-end on AWS with Terraform, **or** stand it up on any plain Ubuntu VPS with a single script — plus a single-binary, VPN-only web dashboard for status, traffic, connection history, a peer map, **live client management**, and proactive alerting.

![Terraform](https://img.shields.io/badge/Terraform-1.14.8-7B42BC?logo=terraform&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-EC2%20%7C%20VPC%20%7C%20SSM-FF9900?logo=amazonaws&logoColor=white)
![Go](https://img.shields.io/badge/Go-single%20binary-00ADD8?logo=go&logoColor=white)
![WireGuard](https://img.shields.io/badge/WireGuard-UDP%2051820-88171A?logo=wireguard&logoColor=white)

<p align="center">
  <img src="docs/dashboard-demo.gif" alt="A tour of the VPN-only dashboard — Overview, Clients (with peer map), System, Network, Events, and About tabs" width="100%">
</p>

<p align="center"><sub>The VPN-only dashboard, tab by tab. Captured from a live instance; public IPs, peer locations, and keys are redacted.</sub></p>

---

## What is this?

Most "WireGuard on AWS" guides are a pile of manual steps — click through a VPC, hand-edit security groups, SSH in to install packages, paste keys. This repo replaces all of that with **reviewable infrastructure-as-code**: one `terraform apply` stands up the network, the EC2 host, the security posture, and a fully-configured WireGuard server. The same install logic is packaged as a portable **`scripts/install.sh`**, so you can also bring up the identical stack on any plain Ubuntu VPS — no AWS, no Terraform.

It also ships something most guides don't: a **lightweight observability dashboard** that runs *on the VPN host, reachable only over the tunnel*, so you can see what your VPN is doing — who's connected, from where, how much traffic, and whether anything is broken — **add/remove/edit clients live**, and get pinged in chat when something breaks.

**Who it's for**

- **DevOps / platform engineers** who want an auditable, best-practice Terraform reference for WireGuard on AWS rather than a one-off script.
- **Privacy-conscious developers** who want their own VPN — full control, no logs, no third-party trust — on AWS or a $5 VPS, without spending a weekend wiring up `iptables`.

---

## Features

**Deploy anywhere**

- 🏗️ **One-command AWS deploy** — VPC, public subnet, routing, security groups, IAM, EC2, and WireGuard config in a single `terraform apply`.
- 💻 **Standalone install** — the same WireGuard + dashboard bootstrap as a portable, env-driven `scripts/install.sh` for any Ubuntu VPS (no AWS/Terraform).
- ♻️ **Install / update / remove lifecycle** — re-run to update **in place** (reuses the server key, preserves the live peer set, no tunnel drop); `--uninstall` (keep data) / `--purge` (full wipe) / `--dashboard-only` to tear down cleanly.
- 🧬 **arm64 by default** — a single `cpu_architecture` toggle (Graviton `t4g.micro` default, or `x86_64`); architecture-agnostic boot picks the right dashboard binary.
- 🔑 **Zero-touch server key** — no manual key step: the instance generates and self-manages its WireGuard server key in SSM (reused across rebuilds), and the private key never touches Terraform state or the launch template. Only the public key is published (SSM + installer output).
- 🔒 **Hardened by default** — no SSH (shell via SSM Session Manager only), IMDSv2 required, encrypted root volume, and a KMS-encrypted state bucket that holds no private keys.
- 📦 **Reproducible & pinned** — Terraform, providers, and the AMI are pinned to exact versions; remote state in S3 with native locking.

**Live client management**

- 👥 **The dashboard is the single source of truth for peers** — add / remove / edit / enable-disable clients at runtime (paste a public key; tunnel IP auto-assigned), applied instantly with `wg syncconf` — **no instance replacement, no downtime, no dropped tunnels**. Terraform doesn't manage the peer list; it just seeds **one admin peer** for first-connect, so editing peers never churns the instance and never shows up as `terraform plan` drift.
- 🖥️ **Add clients from the server shell** — an optional on-box **`wg-peer`** CLI (`add` / `remove` / `update`) drives the same dashboard API as the UI; `wg-peer add <name> --show-config` generates a keypair and prints a ready-to-use client config, perfect for onboarding the first peer on a fresh VPS.
- 💾 **Durable in the cloud, simple locally** — `local` mode keeps peers in on-box SQLite; `cloud` mode additionally mirrors them to a **versioned S3 backup** (write-through on every change, restore on boot) so a rebuilt instance keeps its peers.
- ⬇️ **Client config download** — grab a ready-to-use peer config (full or split tunnel) for any client. The server **never holds client private keys** (except the `wg-peer --show-config` keygen, which prints once and discards).

**Observability & alerting (Go, single static binary)**

- 📊 **Live status** — service health, per-client online/offline, throughput, and recent handshakes resolved to **client names** (one row per peer).
- 🕑 **Connection history** — per-client online/offline timeline, session count, connected time.
- 🗺️ **Offline peer map** — a world map (embedded SVG) of where peers connect from, with zoom & pan. *No external map tiles — zero outbound requests.*
- 🔔 **Proactive alerting** — on service-down, high disk, sustained-high CPU, or a peer over a transfer cap (edge-triggered, with cooldown + recovery). Fans out to a **Slack-compatible webhook, a Slack bot, Telegram, and Discord**; the webhook is manageable (set / test / revert) from the UI. A Prometheus **`/metrics`** endpoint is exposed for external scraping.
- 🎨 **Polished & offline** — a cohesive design system (embedded IBM Plex fonts, light/dark, WCAG-AA), `html/template` + [htmx](https://htmx.org/) + [Chart.js](https://www.chartjs.org/), pure-Go SQLite, embedded GeoIP — no SPA, no build step, no CDNs.

---

## Architecture

```
                    ┌───────────────────────── AWS (us-east-1) ──────────────────────────┐
   you ── WireGuard │  VPC 10.23.0.0/16                                                   │
   client  (UDP     │   └─ public subnet ─ Internet Gateway                               │
         51820) ────┼──▶ EC2 (Ubuntu 24.04, t4g.micro / Graviton by default)              │
                    │     ├─ wg-quick@wg0  (172.16.15.1/24, NAT to internet)              │
                    │     ├─ wireguard-dashboard.service  ──▶ http://172.16.15.1:8080     │
                    │     │     (bound to the tunnel IP — reachable ONLY over the VPN)     │
                    │     └─ IAM role: self-manage server key in SSM (+ Session Manager)   │
                    │  S3: Terraform remote state (native locking, KMS-encrypted)          │
                    │  S3: client-list backup (clients.json) — cloud mode only             │
                    └─────────────────────────────────────────────────────────────────────┘

   …or the same WireGuard + dashboard on any plain Ubuntu VPS via `scripts/install.sh`.
```

- The dashboard binds to the **WireGuard tunnel IP** (`172.16.15.1:8080`), so it's only reachable once you're connected to the VPN — that's the entire access-control model (no login, by design).
- One shared **`scripts/install.sh`** is the source of truth for the install. On AWS, cloud-init fetches and runs it at boot; on a VPS you download and run it yourself — so the two paths can't drift.

---

## Repository layout

```
.
├── scripts/
│   ├── install.sh           # Portable WireGuard + dashboard installer (Ubuntu; the source of truth)
│   └── wg-peer              # On-box CLI to add/remove/update a peer via the local dashboard API
├── terraform/
│   ├── dev/                 # The deployable root module (the environment you apply)
│   │   ├── backend/         #   one-time bootstrap: the S3 state bucket
│   │   ├── locals.tf        #   environment config (region, name, CIDR, tags)
│   │   ├── main.tf          #   composes the network + wireguard modules; admin_peer + mode
│   │   └── …
│   └── modules/
│       ├── network/vpc/     # VPC, subnets, routing, default SG
│       └── wireguard/       # EC2 + IAM + SG + SSM key + S3 client-list backup + cloud-init wrapper
├── dashboard/               # The Go observability + client-management dashboard
│   ├── cmd/wireguard-dashboard/
│   ├── internal/            # alerts, clients, db, geoip, history, poller, server, serverinfo, wgsync, …
│   ├── web/                 # html/template + static assets (htmx, Chart.js, fonts, world.svg)
│   └── Makefile             # build / run / test
└── Makefile                 # repo-wide pre-commit (fmt, tflint, trivy, docs) + shellcheck
```

---

## Prerequisites

- **WireGuard tools** (`wg`, `wg-quick`) on your client machine.
- **For the AWS path:** an AWS account + credentials (exported `AWS_PROFILE`); **[Terraform](https://developer.hashicorp.com/terraform/install) `1.14.8`** (exact — versions are pinned). The server's WireGuard key is generated and self-managed by the instance — there's no manual key step.
- **For the standalone path:** a plain **Ubuntu** VPS with a public IP, root/sudo, and **inbound UDP 51820** open in the provider firewall.
- For the dashboard: a **public GitHub Release** that publishes the `wireguard-dashboard-<arch>` asset (the bundled CI does this), pinned via a release tag. **The dashboard is always installed** alongside WireGuard (the release tag is required) — it's how you manage peers.

---

## Install — Option A: AWS (Terraform)

```bash
git clone https://github.com/vkatrichenko/wireguard-vpn.git
cd wireguard-vpn
export AWS_PROFILE=your-profile   # all terraform/aws commands assume this is set
```

**1. Configure.** Edit [`terraform/dev/locals.tf`](terraform/dev/locals.tf) (region, project name, CIDR, tags) and [`terraform/dev/main.tf`](terraform/dev/main.tf):

```hcl
# terraform/dev/main.tf
# admin_peer seeds exactly ONE bootstrap peer so you can connect and open the
# dashboard on a fresh deploy (anti-lockout). It's seeded only while the store is
# empty; after that it's an ordinary, UI-editable peer. Every OTHER peer is added
# from the dashboard (or the wg-peer script) — Terraform does not manage the list.
# Set it to null to seed no peer and add everyone from the UI.
admin_peer = {
  name       = "laptop"
  public_key = "<peer-public-key>"   # tunnel IP is auto-assigned (172.16.15.2/32)
}

client_management_mode = "cloud"                   # "cloud" = SQLite + S3 backup; "local" = SQLite only
dashboard_release_tag  = "v0.0.16"                 # pin the dashboard version (required)
github_repo            = "vkatrichenko/wireguard-vpn"  # public repo for install.sh + the release binary
# cpu_architecture     = "arm64"                   # default; set "x86_64" for Intel/AMD
```

**Pick a client-management mode** (details in [Client storage: `local` vs `cloud`](#client-storage-local-vs-cloud)):

- **`cloud`** (recommended on AWS) — peers live in on-box SQLite **and** a versioned S3 backup, so a rebuilt/replaced instance restores its peer set. Terraform provisions the bucket + least-privilege IAM automatically; it never reads or reconciles the list (no drift).
- **`local`** — peers live only in on-box SQLite. Simplest; a full instance rebuild starts from just the `admin_peer` seed.

Generate a peer keypair off-host and paste the **public** key above (keep the private key on the client):

```bash
wg genkey | tee privatekey | wg pubkey > publickey
```

> **You don't create a server key.** The instance generates and self-manages its own WireGuard server private key at first boot — it reads the key from SSM if present, or generates one (`wg genkey`) and stores it there if absent. There's no manual `aws ssm put-parameter` step, and the private key never appears in Terraform state or the launch template. After deploy, read the server **public** key from the SSM String param `/config/<project>-<env>/server-public-key` (or the dashboard's server card once connected).

**2. Bootstrap the Terraform state bucket** (one-time, on a fresh clone):

```bash
cd terraform/dev/backend
terraform init
terraform plan -out=tfplan && terraform apply tfplan
```

**3. Deploy the VPN.** From `terraform/dev/`:

```bash
cd ..
terraform init
terraform plan -out=tfplan      # review
terraform apply tfplan          # creates the VPC, EC2, WireGuard, and (if pinned) the dashboard
```

Then **connect** (see [Connect a client](#connect-a-client)) and open the dashboard over the tunnel at **http://172.16.15.1:8080**.

---

## Install — Option B: Any Ubuntu VPS (`install.sh`)

No AWS, no Terraform — the same WireGuard server + dashboard on a plain Ubuntu host.

**1. Prepare the host.** On your VPS provider, open **inbound UDP 51820**. SSH in as a sudo user.

**2. Generate your first client's keypair (on your laptop):**

```bash
wg genkey | tee privatekey | wg pubkey > publickey
cat publickey      # you'll paste this into the dashboard in step 5
```

**3. Download and run the installer (on the VPS).** The dashboard is always installed alongside WireGuard, so the release tag + repo are required:

```bash
curl -fsSL https://raw.githubusercontent.com/vkatrichenko/wireguard-vpn/main/scripts/install.sh -o install.sh
sudo DASHBOARD_RELEASE_TAG="v0.0.16" \
     DASHBOARD_RELEASE_REPO="vkatrichenko/wireguard-vpn" \
     bash install.sh
```

This installs in **`local` mode** by default (peers in on-box SQLite — the natural fit for a standalone VPS) and also installs the **`wg-peer`** helper. Useful env vars: `WG_SERVER_NET` (default `172.16.15.1/24`), `WG_SERVER_PORT` (`51820`), `WG_CLIENT_DNS` (`1.1.1.1`), `WG_PUBLIC_ENDPOINT` (your VPS public IP, to skip auto-discovery). To use `cloud` mode on a VPS (S3 backup), also pass `CLIENT_MANAGEMENT_MODE=cloud CLIENT_STORE_S3_BUCKET=… CLIENT_STORE_S3_KEY=clients.json` with AWS credentials available to the host. The installer prints the **server public key** and an **example client config** when it finishes.

> **You don't pass a server key.** On a standalone VPS the installer **generates the server's WireGuard private key automatically** (`wg genkey`) on first run and persists it to `/etc/wireguard/server.key` (`0600`); every later re-run reuses that same key, so the server identity stays stable. Pass `WG_SERVER_PRIVATE_KEY` only if you want to supply a specific key (e.g. restoring from a backup) — and once chosen, **don't change it on re-runs**, or you'll invalidate every existing client config. _(The AWS path behaves the same way — the instance self-manages its key in SSM — so neither path needs you to supply a server key.)_

**4. Add your first client.** The dashboard is VPN-only, so before you're a peer you have two options:

- **Simplest — the `wg-peer` script (no SSH tunnel):** on the VPS, generate a peer and print its config in one step:

  ```bash
  wg-peer add laptop --show-config       # generates a keypair, prints the full client config
  ```

  Copy the printed config to your device as `wg0.conf` and bring it up (step 5). This is the whole point of `wg-peer` — first-peer onboarding from the shell. (Bring-your-own key instead: `wg-peer add laptop --pubkey "$(cat publickey)"`.)

- **Or the dashboard UI over an SSH tunnel:**

  ```bash
  ssh -L 8080:172.16.15.1:8080 youruser@<vps-public-ip>
  # open http://localhost:8080 → Clients tab → paste the public key from step 2 + a name
  ```

**5. Connect** (see [Connect a client](#connect-a-client)). Once the tunnel is up you're a peer — from then on, reach the dashboard **directly at http://172.16.15.1:8080** and manage every future client from the UI or `wg-peer` (no SSH tunnel needed).

---

## Connect a client

Build `wg0.conf` on your client with the server's **public** IP/key (printed by the installer, `terraform output`, or the dashboard) and the tunnel IP assigned to this client:

```ini
# /etc/wireguard/wg0.conf
[Interface]
PrivateKey = <your client private key>
Address    = 172.16.15.2/32
DNS        = 1.1.1.1               # AWS: the VPC resolver (e.g. 10.23.0.2); VPS: 1.1.1.1

[Peer]
PublicKey  = <server public key>
Endpoint   = <server-public-ip>:51820
AllowedIPs = 0.0.0.0/0, ::/0       # full tunnel (use 172.16.15.0/24 for split)
PersistentKeepalive = 25
```

```bash
sudo wg-quick up wg0
```

Tip: the dashboard's **Config → Full / Split** download fills in everything except your private-key line.

---

## Manage clients

The **dashboard is the sole source of truth** for peers (its on-box SQLite DB), so peers are managed only through the UI or the `wg-peer` script — never through Terraform. Adding, editing, or removing a peer applies live via `wg syncconf` with **no instance replacement and no `terraform plan` drift**. Terraform seeds only the one `admin_peer` (and only while the store is empty).

**From the dashboard UI (Clients tab):** add / edit / remove / enable-disable — paste a public key and a name; the tunnel IP auto-assigns. Applied instantly, other tunnels untouched.

**From the server shell (`wg-peer` script):** an on-box CLI that drives the *same* dashboard API as the UI (so they never diverge). Run it on the VPN host (directly, or via `ssh youruser@<host> 'wg-peer …'`, or `aws ssm start-session` on EC2):

```bash
# Add a peer, server-generate its keypair, and print a ready-to-use client config.
# The private key is shown ONCE and never written to disk/SQLite/S3 — copy it now.
wg-peer add alice --show-config

# Add a peer with your OWN public key (server never sees the private key):
wg-peer add bob --pubkey "$(cat publickey)"

# Rename a peer or rotate its key:
wg-peer update alice --name alice-laptop
wg-peer update alice --pubkey "<new-public-key>"

# Remove a peer:
wg-peer remove bob
```

`wg-peer` needs the dashboard running (it always is) and reaches it at `172.16.15.1:8080` by default (override with `WG_PEER_API_ADDR`). The server never sees a client private key — the only exception is the `--show-config` keygen, which prints the private key once for copy-paste and then discards it.

### Client storage: `local` vs `cloud`

`client_management_mode` (AWS: `main.tf`; VPS: `CLIENT_MANAGEMENT_MODE`) selects where the UI-managed peer list is stored:

| Mode | Storage | Survives instance rebuild? | Notes |
|---|---|---|---|
| **`local`** | On-box SQLite only | No — reseeds from `admin_peer` | Default for a standalone VPS; no AWS needed. |
| **`cloud`** | SQLite **+ versioned S3 backup** | **Yes** — restored from S3 on boot | Every change write-throughs to `s3://…/clients.json`; Terraform provisions the bucket + least-privilege IAM but never reads it (no drift). |

> **Cloud-mode IAM note:** the instance role needs `s3:GetObject` + `s3:PutObject` on `clients.json` **and `s3:ListBucket` on the bucket**. `ListBucket` is required so the dashboard's first-boot read of the not-yet-created object returns a clean `404` (which triggers the cold-seed) instead of a `403` that silently disables the backup. The Terraform module wires all three automatically in `cloud` mode.

---

## Update & uninstall (`install.sh`)

Re-running the installer is a **safe in-place update** — it reuses the existing `server.key`, preserves the live peer set (won't clobber dashboard-added clients), applies changes without dropping tunnels, and restarts the dashboard onto the new binary:

```bash
# update: bump the tag and re-run (don't pass WG_SERVER_PRIVATE_KEY — the persisted key is reused)
sudo DASHBOARD_RELEASE_TAG="v0.0.16" DASHBOARD_RELEASE_REPO="vkatrichenko/wireguard-vpn" bash install.sh

sudo bash install.sh --uninstall        # stop + remove services/artifacts, KEEP data (key, conf, client DB)
sudo bash install.sh --dashboard-only   # remove only the dashboard, leave the VPN up
sudo bash install.sh --purge            # remove AND wipe the server key, wg0.conf, and client DB
```

> EC2 teardown stays `terraform destroy`; `--uninstall`/`--purge` are for standalone-VPS hosts.

---

## Configuration

Deployable AWS config lives in [`terraform/dev/locals.tf`](terraform/dev/locals.tf) and [`main.tf`](terraform/dev/main.tf) (this project uses `locals.tf`, not `terraform.tfvars`).

| Setting | Where | Notes |
|---|---|---|
| Region / project / CIDR / tags | `locals.tf` | Region is intentionally duplicated in the S3 backend block (Terraform can't reference locals there) — change both if you move regions. |
| Admin **bootstrap peer** | `main.tf` → `admin_peer` | One `{ name, public_key }` (or `null`) seeded only while the store is empty; every other peer is managed in the dashboard / `wg-peer`. |
| Client storage mode | `main.tf` → `client_management_mode` | `"local"` (SQLite only) or `"cloud"` (SQLite + S3 backup). See [above](#client-storage-local-vs-cloud). |
| Dashboard version | `main.tf` → `dashboard_release_tag` | Single source of truth for the running build; **required** (the dashboard is always installed). |
| GitHub repo | `main.tf` → `github_repo` | One slug feeding both the `install.sh` fetch and the release download (must be public). |
| CPU architecture | `main.tf` → `cpu_architecture` | `"arm64"` (default, `t4g.micro`) or `"x86_64"` (`t3a.micro`). |
| Server key | SSM `/config/<project>-<env>/default-private-key` (private, instance-owned) + `/config/<project>-<env>/server-public-key` (public, TF-managed shell) | Instance-managed: read-from-SSM or generate-and-store at boot; never in Terraform state or the launch template. The public key is published to SSM for pre-connect retrieval. On a VPS the key is persisted to `/etc/wireguard/server.key`. |

### Alerting (optional)

The dashboard pushes alerts on **service-down, high disk, sustained-high CPU, and per-peer transfer cap** (edge-triggered, with cooldown + recovery). Config is **environment-variable only** (portable across clouds), supplied to the systemd unit via an `EnvironmentFile`:

| Variable | Default | Purpose |
|---|---|---|
| `DASHBOARD_WEBHOOK_URL` | _(unset = no webhook)_ | Slack-compatible incoming webhook. Also settable at runtime from the **About** tab (in-memory, resets on restart). |
| `DASHBOARD_SLACK_BOT_TOKEN` / `_SLACK_CHANNEL` | _(unset)_ | Slack bot (`chat.postMessage`) transport. |
| `DASHBOARD_TELEGRAM_TOKEN` / `_TELEGRAM_CHAT_ID` | _(unset)_ | Telegram transport. |
| `DASHBOARD_DISCORD_WEBHOOK_URL` | _(unset)_ | Discord transport. |
| `DASHBOARD_ALERT_DISK_PCT` | `90` | High-disk threshold. |
| `DASHBOARD_ALERT_CPU_PCT` / `_CPU_SUSTAIN` | `90` / `5m` | Sustained-CPU threshold + window. |
| `DASHBOARD_ALERT_TRANSFER_BYTES` | `50GiB` | Per-peer transfer cap. |

All transports are opt-in and a silent no-op when unset; alerts are always visible in the dashboard, and a Prometheus **`GET /metrics`** endpoint exposes current values for external scraping.

---

## Development

**Repo-wide quality gate** (runs `terraform fmt`, `terraform-docs`, `tflint`, and `trivy` in Docker; plus `make shellcheck` for `scripts/*.sh`):

```bash
make pre-commit
make shellcheck
```

**The dashboard** (from `dashboard/`):

```bash
make build      # static linux binary (CGO-free); fetches the GeoIP DB on first run
make test       # go test ./...
make run        # local dev — serves on 127.0.0.1:8080

LISTEN_ADDR=127.0.0.1:8080 DB_PATH=/tmp/wgd.db go run ./cmd/wireguard-dashboard
```

> On macOS the host-data cards show "failed to load" (no `/proc`, `wg`, or `systemd`) — expected; the UI still renders for design/preview work.

**Conventions** (enforced): exact version pins (no ranges); `plan -out=tfplan` → review → `apply tfplan` (never bare `apply`; apply is manual/local, no CI apply); every resource carries `Environment`, `Project`, `Owner`, `Managed` via provider `default_tags`.

---

## Security model

- **Access is the tunnel.** The dashboard binds to `172.16.15.1:8080` (the WireGuard interface), so it's unreachable except over the VPN. There is no in-band auth — connecting to the VPN *is* the authentication. The write surfaces (webhook management + client management) inherit this VPN-only trust.
- **No SSH (on AWS).** Port 22 is not exposed and there is **no SSH key material at all** — the keypair, EC2 key pair, and its SSM parameter are gone. Instance shell access is via SSM Session Manager (`aws ssm start-session`), which is IAM-gated and CloudTrail-audited.
- **The server key never leaves the box's control.** The instance **self-manages** its WireGuard server private key — it reads it from SSM at boot, or generates one (`wg genkey`) and stores it if absent. The private key **never appears in Terraform state or the EC2 launch template**; only the non-secret **public** key is published (to an SSM String param + the installer's stdout) so you can build a client config before your first connection. On a VPS the key is a `0600` `/etc/wireguard/server.key`.
- **Hardened instance + state.** IMDSv2 is required (token-only, hop limit 1); the root EBS volume is encrypted; the Terraform state bucket is KMS-encrypted (so object-read alone can't disclose it) and, with the server + SSH keys now out of state, holds no private keys. Alert secrets are env/SSM-supplied and never logged in full or rendered in the UI.
- **The dashboard holds no client private keys** and makes **no outbound requests** for its own operation (embedded map + GeoIP, no CDNs) — the only egress it adds is the opt-in alert transports and the off-AWS public-IP lookup (skippable via `WG_PUBLIC_ENDPOINT`).

---

## Status & roadmap

The deployable AWS environment, the standalone installer, and the full dashboard feature set — including **UI-first client management** (dashboard + `wg-peer` as the sole peer authority, an `admin_peer` bootstrap seed, and `local` / `cloud` S3-backup storage modes), **automatic server-key management** (instance self-manages its key in SSM — no manual bootstrap, key never in Terraform state), a **security-hardening pass** (SSH removed → SSM Session Manager only, IMDSv2-required, encrypted root volume + KMS state bucket), and the **install/update/remove lifecycle** — are implemented and in use (specs 002–020; current dashboard release **`v0.0.16`**, with spec 020's dashboard fixes shipping in the next release). Detailed product / architecture / roadmap notes live under [`context/product/`](context/product/), and per-feature specs under [`context/spec/`](context/spec/).

---

## Contributing

Contributions are welcome. Please:

- Work on a branch and open a **PR** (`feat/…`, `fix/…`, `infra/…`, `docs/…`, `refactor/…`, `chore/…`); use Conventional-Commit-style messages.
- Run `make pre-commit` (and `make test` in `dashboard/`) before pushing.
- Keep Terraform changes plan-reviewable and follow the exact-pin / tagging conventions above.

---

## License

This project is licensed under the **Apache License 2.0** — see [`LICENSE`](LICENSE) for the full text. Copyright 2026 Vladyslav Katrychenko.

Bundled third-party components retain their own licenses and are attributed in [`NOTICE`](NOTICE) (reconciled with [`dashboard/web/static/VENDORED.txt`](dashboard/web/static/VENDORED.txt)):

- **IBM Plex Sans / Mono** — SIL Open Font License 1.1
- **World map outline** (Natural Earth–derived `world.svg`) — Public Domain
- **GeoIP data** — [DB-IP IP-to-City Lite](https://db-ip.com/db/lite.php), CC BY 4.0 (fetched at build, not committed)

---

## Acknowledgements

[WireGuard](https://www.wireguard.com/) · [DB-IP](https://db-ip.com/) · [IBM Plex](https://www.ibm.com/plex/) · [Natural Earth](https://www.naturalearthdata.com/) · [htmx](https://htmx.org/) · [Chart.js](https://www.chartjs.org/) · [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)
