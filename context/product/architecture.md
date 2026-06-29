# System Architecture Overview: wireguard-vpn

---

## 1. Infrastructure & Provisioning

- **Infrastructure as Code:** Terraform 1.14.8 (exact pin, no ranges)
- **Cloud Provider:** AWS via `hashicorp/aws` provider 6.41.0 (exact pin)
- **Target Region:** us-east-1
- **State Backend:** S3 with native locking (`use_lockfile = true`), no DynamoDB
- **Module Strategy:** Local module paths (`../modules/...`); versioned git refs planned for cross-repo reuse

---

## 2. Network & Security

- **VPC Topology:** Single VPC with one public subnet — no NAT gateway, no private subnets
- **Routing:** Internet gateway with a default route in the public subnet route table
- **Default Security Group:** Locked down to deny all traffic; explicit allow-rules only
- **WireGuard Traffic:** UDP 51820 open to 0.0.0.0/0 via dedicated security group
- **Instance Access:** SSM Session Manager only — no SSH port (22) exposed
- **VPN Protocol:** WireGuard (kernel-level, UDP-based, modern cryptography)

---

## 3. Compute & Configuration

- **CPU Architecture (spec 013):** Selectable via a single `cpu_architecture` variable on the `wireguard` module (`"arm64"` | `"x86_64"`, validated), **default `arm64`**. An `arch_config` map derives the AMI name suffix, the AMI `architecture` filter, and the default instance type from that one value. `dev/` does not set it today, so it inherits the module default (arm64).
- **Instance Type:** Arch-derived default — `arm64` → **`t4g.micro`** (Graviton), `x86_64` → `t3a.micro`. Overridable via the module's optional `instance_type` for sizing without touching the module.
- **AMI:** Resolved by an `aws_ami` data source in the `wireguard` module — Canonical Ubuntu Noble 24.04, `most_recent = true`, filtered by the arch-derived suffix + `architecture`. An explicit `ami_id` override (count-gated) preserves explicit pinning. _**Deviation note:** this `most_recent` default differs from the repo's "AMIs pinned explicitly, no `most_recent`" convention (CLAUDE.md); the `ami_id` override is the convention-compliant path._
- **Configuration Method (spec 014):** A single committed, env-driven, Ubuntu-only `scripts/install.sh` is the source of truth for the install (WireGuard server + optional dashboard, fail-hard, shellcheck-clean). Two consumers of the **same script**:
  - **EC2:** cloud-init user-data is a thin AWS wrapper (IMDSv2, SSM-sourced server key, S3 `.ready` signal, EIP, awscli) that **fetches `install.sh` from raw GitHub at a content-pinned ref** (`install_script_ref`, default `main`; `install_script_sha256` verifies the fetched script before it runs) and executes it. The 16 KB user-data cap drove fetch-at-boot over embedding.
  - **Standalone VPS:** download `install.sh`, review, `sudo bash install.sh` — same result on any plain Ubuntu host, no AWS/Terraform.
- **Architecture-agnostic boot:** the host detects its arch at runtime (`uname -m`) and selects the matching AWS CLI installer and the matching `wireguard-dashboard-$GOARCH` release asset (checksum-verified, fail-hard on mismatch).
- **Client Management:** Configurable `clients_config` list in Terraform, each entry with a unique public key and IP assignment. _(Runtime UI-based client management is specified in spec 015 — not yet implemented.)_

---

## 4. Secrets & State Management

- **Server Private Key:** AWS SSM Parameter Store (SecureString) at `/config/wireguard/default-private-key`, created manually outside Terraform
- **Client Keys:** Generated off-host (`wg genkey | tee privatekey | wg pubkey > publickey`), public keys added to Terraform config
- **IAM Access:** Instance role with scoped SSM `GetParameter` permission for the private key parameter
- **Terraform State:** S3 bucket `wireguard-vpn-test-tf-states` with `prevent_destroy = true`, bootstrapped via separate root module (`terraform/dev/backend/`)

---

## 5. Code Quality & Tooling

- **Pre-Commit Framework:** `antonbabenko/pre-commit-terraform` v1.105.0 (runs in Docker via `make pre-commit`)
- **Formatting:** `terraform fmt -recursive`
- **Linting:** tflint
- **Security Scanning:** Trivy (currently warn-only, `--exit-code=0`; HIGH/CRITICAL findings treated as real)
- **Documentation:** terraform-docs (auto-generated module docs)
- **Validation:** `terraform validate` in every affected root module before claiming done

---

## 6. Dashboard Application (Observability)

- **Purpose:** VPN-only ops dashboard — peer status, throughput, connection history, peer geo map, and proactive alerting (specs 002 / 003 / 006 / 007). **Read-only except** the runtime webhook-management write endpoints added in 008 (see §8). **No authentication** — access is gated solely by the WireGuard tunnel.
- **Language & HTTP:** Go std-lib `net/http` (no web framework); server-rendered HTML via `html/template`.
- **Frontend:** htmx partial refreshes on a 10s tick + Chart.js for throughput/timeline charts. No SPA, no build step; all assets vendored, **zero external CDNs/fonts/scripts**.
- **Design system (spec 009):** a token-driven CSS system (`@layer` + `:root`/`[data-theme]` tokens — fluid `clamp()` type scale, spacing, elevation, motion) with **embedded IBM Plex Sans/Mono** (SIL OFL, subset woff2 via `go:embed`), an amber-on-graphite "precision instrument" palette, fluid responsiveness (phone→ultrawide via `clamp()`/`@container`), and WCAG-AA contrast in both themes.
- **Packaging:** Single static binary (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`); web assets + GeoIP DB bundled via `go:embed`. CGO-free SQLite keeps the build glibc-free.
- **Metrics store:** `modernc.org/sqlite` (pure-Go) at `/var/lib/wireguard-dashboard`. Tables `system_metrics`, `traffic_metrics`, `client_traffic` (PK `ts,public_key`), `handshake_events` (`ts,public_key,name`); each `ts`-indexed. **Alert state adds no tables** — it is in-memory only (§8).
- **Poller:** background sampler + hourly retention sweep (`PruneBefore`, ~8d horizon to back the 7d chart range) — no unbounded growth. Also drives the alert evaluator each tick (§8).
- **Geolocation:** embedded **DB-IP IP-to-City Lite** (GeoIP2/MMDB schema via `oschwald/geoip2-golang`) → country/city + lat/lon, fully offline. Migrated from GeoLite2 (006).
- **Views:** Overview / Clients / System / Network / Events / About tabs. Overview carries an **active-alerts strip** (007); per-client expand panel = throughput chart + connection timeline (online/offline bands) + history summary (online/last-seen, session count, connected time); offline world-map card (embedded SVG + equirectangular-projected markers, with bounded zoom/pan via CSS transforms — buttons + wheel/pinch/drag) on the Clients tab (006/010); **webhook-management card on About** (008).
- **Host data sources:** read-only `wg show wg0 dump`/`public-key` + `systemctl is-active/show wg-quick@wg0` via scoped NOPASSWD sudoers; client manifest `/etc/wireguard-dashboard/clients.json` (0640). **Never holds client private keys.**

---

## 7. Dashboard Build & Deployment

- **Distribution (spec 005 / 013):** public GitHub Release artifact from `vkatrichenko/wireguard-vpn`, pinned via `dashboard_release_tag` in `terraform/dev/main.tf` (currently **`v0.0.7`**) — single reviewable source of truth, same explicit-pin philosophy as the AMI. Each release publishes **both** `wireguard-dashboard-amd64` and `wireguard-dashboard-arm64` under one `SHA256SUMS` (spec 013). Replaced the earlier private S3-artifact + ECR path.
- **Install (via `scripts/install.sh`):** the installer downloads `wireguard-dashboard-$GOARCH` + `SHA256SUMS` from the pinned release and verifies the checksum before install; provisioned only when `dashboard_release_tag` is non-empty. On EC2 this runs inside the fetched shared script (spec 014); on a VPS it is the same code path.
- **Service:** systemd `wireguard-dashboard.service`, `Requires`/`After=wg-quick@wg0`; runs as a dedicated `wireguard-dashboard` system user (nologin). Alert config is supplied via an optional `EnvironmentFile=-/etc/wireguard-dashboard/alerts.env` (008, §8).
- **Binding & access:** `LISTEN_ADDR=172.16.15.1:8080` — bound to the WireGuard tunnel IP, so reachable only over the VPN (no public listener; this is why no in-band auth is needed, including for the 008 write endpoints).

---

## 8. Alerting & Webhook Delivery (specs 007 / 008 / 012)

- **Evaluator:** a pure, in-memory per-condition state machine (`internal/alerts`) the poller drives once per tick from already-collected state. **Four watched conditions:** service-down, high-disk, sustained-CPU, per-peer cumulative-transfer (per client). Edge-triggered `OK↔FIRING` with a per-condition cooldown (default 30m) and a single recovery; **state is in-memory only (no alert DB)** and re-arms from current state on restart.
- **Thresholds:** env-configurable, non-secret — `DASHBOARD_ALERT_DISK_PCT`, `_CPU_PCT`, `_CPU_SUSTAIN`, `_TRANSFER_BYTES` — with documented defaults (90% / 90%×5m / 50 GiB).
- **Delivery:** a **`MultiNotifier` fan-out** delivers each alert to all configured transports behind the `Notifier` interface: the runtime-managed **Slack-compatible incoming webhook** (008, `{"text":…}`) **plus** the opt-in, boot-config transports added in 012 — **Slack bot** (`chat.postMessage`), **Telegram**, and **Discord**. Each transport is env/SSM-seeded, redacts its secret in logs, never persists or renders it in full, and is a **silent no-op when unset** (so any subset can be enabled independently and alerts still surface in-UI when none are). Dispatched **off the poll critical path** (bounded buffered channel + worker) with per-attempt timeout + bounded retry.
- **Webhook config (008):** the URL is **env/SSM-seeded at boot** and held in a thread-safe `WebhookConfig` holder; the About-tab card can **set / test / revert** it at runtime as an **in-memory override that is never persisted** (re-seeds from env/SSM on restart). The `POST /api/webhook*` routes are the dashboard's **first write endpoints** — a deliberate, bounded exception to the read-only posture (outbound-target only; still no auth, still no inbound surface).
- **Config provisioning:** the webhook secret + alert knobs reach the host via **Terraform reading SSM and rendering a systemd `EnvironmentFile`** (mirrors the server-key SSM pattern; no instance IAM grant). The 012 transport secrets (Slack bot token, Telegram token, Discord webhook URL) follow the **same SSM→`EnvironmentFile` pattern** — each is a count-gated `aws_ssm_parameter` read at apply, seeded into `alerts.env` only when its param NAME is wired; the non-secret channel/chat-id are plain string vars seeded the same way. The Go binary stays **cloud-agnostic** — it reads only env vars (host label is `os.Hostname()`, not IMDSv2), so the same binary runs on any cloud/VPS.
- **Prometheus `GET /metrics` (012):** a hand-rolled text-exposition endpoint (no client library) under the `wireguard_` namespace, emitting only the **current in-memory values** — no per-scrape exec or DB query. **VPN-only and unauthenticated** like the rest of the dashboard, and distinct from the Chart.js `/api/metrics*` JSON routes (which feed the in-UI charts).
- **Status:** 007 and 008 are deployed and operator-verified (2026-06-25) — webhook delivery + the About-tab Set/Test confirmed against a live Slack webhook. 012 (multi-transport fan-out + `/metrics` + peer-down removal) is **code-complete pending deploy**.
