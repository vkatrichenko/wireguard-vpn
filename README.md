# WireGuard VPN on AWS — with a built-in observability dashboard

> A fully codified, self-hosted [WireGuard](https://www.wireguard.com/) VPN server on AWS, provisioned end-to-end with Terraform — plus a single-binary, VPN-only web dashboard for status, traffic, connection history, a peer map, and proactive alerting.

![Terraform](https://img.shields.io/badge/Terraform-1.14.8-7B42BC?logo=terraform&logoColor=white)
![AWS](https://img.shields.io/badge/AWS-EC2%20%7C%20VPC%20%7C%20SSM-FF9900?logo=amazonaws&logoColor=white)
![Go](https://img.shields.io/badge/Go-1.25.5-00ADD8?logo=go&logoColor=white)
![WireGuard](https://img.shields.io/badge/WireGuard-UDP%2051820-88171A?logo=wireguard&logoColor=white)

---

## What is this?

Most "WireGuard on AWS" guides are a pile of manual steps — click through a VPC, hand-edit security groups, SSH in to install packages, paste keys. This repo replaces all of that with **reviewable infrastructure-as-code**: one `terraform apply` stands up the network, the EC2 host, the security posture, and a fully-configured WireGuard server.

It also ships something most of those guides don't: a **lightweight observability dashboard** that runs *on the VPN host, reachable only over the tunnel*, so you can actually see what your VPN is doing — who's connected, from where, how much traffic, and whether anything is broken — and get pinged in chat when it is.

**Who it's for**

- **DevOps / platform engineers** who want an auditable, best-practice Terraform reference for WireGuard on AWS rather than a one-off script.
- **Privacy-conscious developers** who want their own VPN — full control, no logs, no third-party trust — without spending a weekend wiring up `iptables`.

---

## Features

**Infrastructure (Terraform)**

- 🏗️ **One-command deploy** — VPC, public subnet, routing, security groups, IAM, EC2, and WireGuard config in a single `terraform apply`.
- 🔐 **Secure key handling** — the server's private key lives in **AWS SSM Parameter Store** (SecureString) and is read at boot; never committed.
- 👥 **Multi-client** — declare peers in a typed list; each gets a unique key and tunnel IP.
- 🚪 **No SSH exposure** — instance access is via **SSM Session Manager**; only WireGuard's UDP **51820** is open.
- 📦 **Reproducible** — Terraform, providers, and the AMI are **pinned to exact versions**; remote state in S3 with native locking.

**Dashboard (Go, single static binary)**

- 📊 **Live status** — service health, per-client online/offline, throughput, recent handshakes.
- 🕑 **Connection history** — per-client online/offline timeline, session count, connected time.
- 🗺️ **Offline peer map** — a world map (embedded SVG) of where peers connect from, with **zoom & pan** to country level. *No external map tiles — zero outbound requests.*
- 🔔 **Proactive alerting** — push to a Slack-compatible webhook on service-down, high disk, sustained-high CPU, a dropped peer, or a peer over a transfer cap. Edge-triggered, with cooldown and recovery. The webhook is manageable (set / test / revert) from the UI.
- ⬇️ **Client config download** — grab a ready-to-use peer config (full or split tunnel) from the Clients tab.
- 🎨 **Polished & responsive** — a cohesive design system (embedded IBM Plex fonts, light/dark, WCAG-AA), fluid from phone to ultrawide.
- 🪶 **Single binary, fully offline** — `html/template` + [htmx](https://htmx.org/) + [Chart.js](https://www.chartjs.org/), pure-Go SQLite, embedded GeoIP — no SPA, no build step, no CDNs, and it **never holds client private keys**.

---

## Architecture

```
                    ┌───────────────────────── AWS (us-east-1) ──────────────────────────┐
   you ── WireGuard │  VPC 10.23.0.0/16                                                   │
   client  (UDP     │   └─ public subnet ─ Internet Gateway                               │
         51820) ────┼──▶ EC2 (Ubuntu, t3.micro)                                           │
                    │     ├─ wg-quick@wg0  (172.16.15.1/24, NAT to internet)              │
                    │     ├─ wireguard-dashboard.service  ──▶ http://172.16.15.1:8080     │
                    │     │     (bound to the tunnel IP — reachable ONLY over the VPN)     │
                    │     └─ IAM role: read server key from SSM (+ SSM Session Manager)    │
                    │  S3: Terraform remote state (native locking)                         │
                    └─────────────────────────────────────────────────────────────────────┘
```

- The dashboard binds to the **WireGuard tunnel IP** (`172.16.15.1:8080`), so it's only reachable once you're connected to the VPN — that's the entire access-control model (no login, by design).
- The dashboard is distributed as a **verified GitHub Release binary**, downloaded and SHA256-checked by cloud-init at first boot (pinned via `dashboard_release_tag`).

---

## Repository layout

```
.
├── terraform/
│   ├── dev/                 # The deployable root module (the environment you apply)
│   │   ├── backend/         #   one-time bootstrap: the S3 state bucket
│   │   ├── locals.tf        #   environment config (region, name, CIDR, tags)
│   │   ├── main.tf          #   composes the network + wireguard modules; client list
│   │   └── …
│   └── modules/
│       ├── network/vpc/     # VPC, subnets, routing, default SG
│       └── wireguard/       # EC2 + IAM + SG + SSM key + cloud-init user-data
├── dashboard/               # The Go observability dashboard
│   ├── cmd/wireguard-dashboard/
│   ├── internal/            # alerts, geoip, history, poller, server, wg, …
│   ├── web/                 # html/template + static assets (htmx, Chart.js, fonts, world.svg)
│   └── Makefile             # build / run / test
└── Makefile                 # repo-wide pre-commit (fmt, tflint, trivy, docs)
```

---

## Prerequisites

- An **AWS account** and credentials (the examples assume an exported `AWS_PROFILE`).
- **[Terraform](https://developer.hashicorp.com/terraform/install) `1.14.8`** (exact — versions are pinned).
- **WireGuard tools** (`wg`, `wg-quick`) on your client machine.
- A **server private key in SSM** — created out-of-band (see step 2), mirroring how secrets are kept out of the repo.
- For the dashboard binary: either a **public GitHub Release** of your fork that publishes the `wireguard-dashboard` asset (the bundled CI does this), **or** build it yourself (see [Development](#development)). Leave `dashboard_release_tag` empty to skip the dashboard entirely.

---

## Quick start

```bash
git clone https://github.com/vkatrichenko/wireguard-vpn.git
cd wireguard-vpn
export AWS_PROFILE=your-profile   # all terraform/aws commands assume this is set
```

**1. Configure.** Edit [`terraform/dev/locals.tf`](terraform/dev/locals.tf) (region, project name, CIDR, tags) and [`terraform/dev/main.tf`](terraform/dev/main.tf):

```hcl
# terraform/dev/main.tf — add your peers (public keys generated off-host, below)
clients_config = [
  { name = "laptop", address = "172.16.15.6/32", public_key = "<peer-public-key>" },
  { name = "phone",  address = "172.16.15.7/32", public_key = "<peer-public-key>" },
]

dashboard_release_tag  = "v0.0.4"                  # pin the dashboard version ("" disables it)
dashboard_release_repo = "your-org/wireguard-vpn"  # the public repo the release binary is pulled from
```

Generate a peer keypair off-host and paste the **public** key above (keep the private key on the client):

```bash
wg genkey | tee privatekey | wg pubkey > publickey
```

**2. Create the server's WireGuard private key in SSM** (one-time, not managed by Terraform):

```bash
SERVER_PRIV=$(wg genkey)
aws ssm put-parameter \
  --name "/config/wireguard-vpn-test/default-private-key" \
  --type SecureString --value "$SERVER_PRIV"
```

**3. Bootstrap the Terraform state bucket** (one-time, on a fresh clone):

```bash
cd terraform/dev/backend
terraform init
terraform plan -out=tfplan && terraform apply tfplan
```

**4. Deploy the VPN.** From `terraform/dev/`:

```bash
cd ..
terraform init
terraform plan -out=tfplan      # review the plan
terraform apply tfplan          # creates the VPC, EC2, WireGuard, and (if pinned) the dashboard
```

**5. Connect.** Build a client config with the server's **public** IP/key (both shown by `terraform output` / the dashboard) and bring the tunnel up:

```ini
# /etc/wireguard/wg0.conf
[Interface]
PrivateKey = <your client private key>
Address    = 172.16.15.6/32
DNS        = 10.23.0.2

[Peer]
PublicKey  = <server public key>
Endpoint   = <server-public-ip>:51820
AllowedIPs = 0.0.0.0/0           # full tunnel (use 172.16.15.0/24, 10.23.0.0/16 for split)
```

```bash
sudo wg-quick up wg0
```

**6. Open the dashboard** — over the tunnel — at **http://172.16.15.1:8080**.

---

## Configuration

All deployable config lives in [`terraform/dev/locals.tf`](terraform/dev/locals.tf) and [`main.tf`](terraform/dev/main.tf) (this project uses `locals.tf`, not `terraform.tfvars`).

| Setting | Where | Notes |
|---|---|---|
| Region / project / CIDR / tags | `locals.tf` | Region is intentionally duplicated in the S3 backend block (Terraform can't reference locals there) — change both if you move regions. |
| Peers | `main.tf` → `clients_config` | One entry per client: `name`, tunnel `address`, `public_key`. |
| Dashboard version | `main.tf` → `dashboard_release_tag` | The single source of truth for the running dashboard build (`""` = no dashboard). |
| Server key | SSM `/config/<project>-<env>/default-private-key` | Created out-of-band; read at boot. |

### Alerting (optional)

The dashboard pushes alerts to a **Slack-compatible incoming webhook** when set. Configuration is **environment-variable only** (so it's portable across clouds) and supplied to the systemd unit via an `EnvironmentFile`:

| Variable | Default | Purpose |
|---|---|---|
| `DASHBOARD_WEBHOOK_URL` | _(unset = alerting disabled)_ | Slack-compatible webhook; the secret. Also settable at runtime from the **About** tab (in-memory, resets on restart). |
| `DASHBOARD_ALERT_DISK_PCT` | `90` | High-disk threshold. |
| `DASHBOARD_ALERT_CPU_PCT` / `_CPU_SUSTAIN` | `90` / `5m` | Sustained-CPU threshold + window. |
| `DASHBOARD_ALERT_PEER_STALE` | `10m` | Peer-down staleness threshold. |
| `DASHBOARD_ALERT_TRANSFER_BYTES` | `50GiB` | Per-peer transfer cap. |

With no webhook configured, alerts are still visible in the dashboard — nothing is sent (opt-in).

---

## Development

**Repo-wide quality gate** (runs `terraform fmt`, `terraform-docs`, `tflint`, and `trivy` in Docker):

```bash
make pre-commit
```

**The dashboard** (from `dashboard/`):

```bash
make build      # static linux/amd64 binary (CGO-free); fetches the GeoIP DB on first run
make test       # go test ./...
make run        # local dev — serves on 127.0.0.1:8080

# Run locally with a writable DB path + a sample client manifest:
LISTEN_ADDR=127.0.0.1:8080 DB_PATH=/tmp/wgd.db \
  go run ./cmd/wireguard-dashboard
```

> On macOS the data cards show "failed to load" (no `/proc`, `wg`, or `systemd`) — expected; the UI itself still renders for design/preview work.

**Conventions** (enforced):

- **Exact version pins** — no `~>` or ranges, for Terraform, providers, and the AMI.
- **Apply workflow** — always `plan -out=tfplan` → review → `apply tfplan`. Never a bare `apply`. Apply is **manual and local**; there is no CI apply.
- **Tagging** — every resource carries `Environment`, `Project`, `Owner`, `Managed` via provider `default_tags`.

---

## Security model

- **Access is the tunnel.** The dashboard binds to `172.16.15.1:8080` (the WireGuard interface), so it's unreachable except over the VPN. There is no in-band auth — connecting to the VPN *is* the authentication. (The webhook-management UI is the one write surface; it inherits this VPN-only trust.)
- **No SSH.** Instance access is via SSM Session Manager; port 22 is not exposed.
- **Secrets stay out of the repo.** The server key is an SSM SecureString; the alert webhook is env/SSM-supplied and never logged in full or rendered in the UI.
- **The dashboard holds no client private keys** and makes **no outbound requests** for its own operation (embedded map + GeoIP, no CDNs) — the only egress it adds is the opt-in alert webhook.

---

## Status & roadmap

The deployable environment and the full dashboard feature set are implemented and in use. Detailed product/architecture/roadmap notes live under [`context/product/`](context/product/), and per-feature specs under [`context/spec/`](context/spec/).

---

## Contributing

Contributions are welcome. Please:

- Work on a branch and open a **PR** (`feat/…`, `fix/…`, `infra/…`, `docs/…`, `refactor/…`, `chore/…`); use Conventional-Commit-style messages.
- Run `make pre-commit` (and `make test` in `dashboard/`) before pushing.
- Keep Terraform changes plan-reviewable and follow the exact-pin / tagging conventions above.

---

## License

> **No project license file is committed yet.** Add a `LICENSE` (e.g. MIT or Apache-2.0) before relying on this repository.

Bundled third-party assets retain their own licenses (recorded in [`dashboard/web/static/VENDORED.txt`](dashboard/web/static/VENDORED.txt)):

- **IBM Plex Sans / Mono** — SIL Open Font License 1.1
- **World map outline** (Natural Earth–derived `world.svg`) — Public Domain
- **GeoIP data** — [DB-IP IP-to-City Lite](https://db-ip.com/db/lite.php), CC BY 4.0 (fetched at build, not committed)

---

## Acknowledgements

[WireGuard](https://www.wireguard.com/) · [DB-IP](https://db-ip.com/) · [IBM Plex](https://www.ibm.com/plex/) · [Natural Earth](https://www.naturalearthdata.com/) · [htmx](https://htmx.org/) · [Chart.js](https://www.chartjs.org/) · [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)
