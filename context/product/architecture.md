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

- **Instance Type:** t3.micro (2 vCPU, 1 GB RAM, free-tier eligible)
- **AMI:** Explicitly pinned in locals.tf (no `most_recent = true` data source)
- **Configuration Method (Current):** Cloud-init user-data — installs and configures WireGuard at first boot, retrieves private key from SSM
- **Alternatives Under Evaluation:**
  - **Packer** — pre-baked AMI with WireGuard installed; faster boot, immutable infrastructure pattern; adds a build pipeline step
  - **Ansible** — post-provisioning configuration; allows re-configuration without instance replacement; adds tool dependency
- **Client Management:** Configurable `clients_config` list in Terraform, each entry with a unique public key and IP assignment

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
- **Packaging:** Single static binary (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`); web assets + GeoIP DB bundled via `go:embed`. CGO-free SQLite keeps the build glibc-free.
- **Metrics store:** `modernc.org/sqlite` (pure-Go) at `/var/lib/wireguard-dashboard`. Tables `system_metrics`, `traffic_metrics`, `client_traffic` (PK `ts,public_key`), `handshake_events` (`ts,public_key,name`); each `ts`-indexed. **Alert state adds no tables** — it is in-memory only (§8).
- **Poller:** background sampler + hourly retention sweep (`PruneBefore`, ~8d horizon to back the 7d chart range) — no unbounded growth. Also drives the alert evaluator each tick (§8).
- **Geolocation:** embedded **DB-IP IP-to-City Lite** (GeoIP2/MMDB schema via `oschwald/geoip2-golang`) → country/city + lat/lon, fully offline. Migrated from GeoLite2 (006).
- **Views:** Overview / Clients / System / Network / Events / About tabs. Overview carries an **active-alerts strip** (007); per-client expand panel = throughput chart + connection timeline (online/offline bands) + history summary (online/last-seen, session count, connected time); offline world-map card (embedded SVG + equirectangular-projected markers) on the Clients tab (006); **webhook-management card on About** (008).
- **Host data sources:** read-only `wg show wg0 dump`/`public-key` + `systemctl is-active/show wg-quick@wg0` via scoped NOPASSWD sudoers; client manifest `/etc/wireguard-dashboard/clients.json` (0640). **Never holds client private keys.**

---

## 7. Dashboard Build & Deployment

- **Distribution (spec 005):** public GitHub Release artifact from `vkatrichenko/wireguard-vpn`, pinned via `dashboard_release_tag` in `terraform/dev/main.tf` (currently `v0.0.3`) — single reviewable source of truth, same explicit-pin philosophy as the AMI. Replaced the earlier private S3-artifact + ECR path.
- **Install (cloud-init):** user-data downloads the binary + `SHA256SUMS` from the pinned release and verifies the checksum before install; provisioned only when `dashboard_release_tag` is non-empty.
- **Service:** systemd `wireguard-dashboard.service`, `Requires`/`After=wg-quick@wg0`; runs as a dedicated `wireguard-dashboard` system user (nologin). Alert config is supplied via an optional `EnvironmentFile=-/etc/wireguard-dashboard/alerts.env` (008, §8).
- **Binding & access:** `LISTEN_ADDR=172.16.15.1:8080` — bound to the WireGuard tunnel IP, so reachable only over the VPN (no public listener; this is why no in-band auth is needed, including for the 008 write endpoints).

---

## 8. Alerting & Webhook Delivery (specs 007 / 008)

- **Evaluator:** a pure, in-memory per-condition state machine (`internal/alerts`) the poller drives once per tick from already-collected state. **Five watched conditions:** service-down, high-disk, sustained-CPU, peer-down (per client), per-peer cumulative-transfer (per client). Edge-triggered `OK↔FIRING` with a per-condition cooldown (default 30m) and a single recovery; **state is in-memory only (no alert DB)** and re-arms from current state on restart.
- **Thresholds:** env-configurable, non-secret — `DASHBOARD_ALERT_DISK_PCT`, `_CPU_PCT`, `_CPU_SUSTAIN`, `_PEER_STALE`, `_TRANSFER_BYTES` — with documented defaults (90% / 90%×5m / 10m / 50 GiB).
- **Delivery:** outbound HTTPS POST to a **Slack-compatible incoming webhook** (`{"text":…}`), behind a `Notifier` interface so a bot/other transport is a later drop-in. Dispatched **off the poll critical path** (bounded buffered channel + worker) with per-attempt timeout + bounded retry. The URL is a **secret** — redacted in logs, never persisted, never rendered in full. **Opt-in:** no URL → delivery is a silent no-op while alerts still surface in-UI.
- **Webhook config (008):** the URL is **env/SSM-seeded at boot** and held in a thread-safe `WebhookConfig` holder; the About-tab card can **set / test / revert** it at runtime as an **in-memory override that is never persisted** (re-seeds from env/SSM on restart). The `POST /api/webhook*` routes are the dashboard's **first write endpoints** — a deliberate, bounded exception to the read-only posture (outbound-target only; still no auth, still no inbound surface).
- **Config provisioning:** the webhook secret + alert knobs reach the host via **Terraform reading SSM and rendering a systemd `EnvironmentFile`** (mirrors the server-key SSM pattern; no instance IAM grant). The Go binary stays **cloud-agnostic** — it reads only env vars (host label is `os.Hostname()`, not IMDSv2), so the same binary runs on any cloud/VPS.
- **Status:** 007 is code-complete (pending deploy + manual E2E); 008 is specified.
