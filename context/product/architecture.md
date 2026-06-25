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

- **Purpose:** Read-only, VPN-only ops dashboard — peer status, throughput, connection history, peer geo map. No auth, no write/control ops (specs 002 / 003 / 006).
- **Language & HTTP:** Go std-lib `net/http` (no web framework); server-rendered HTML via `html/template`.
- **Frontend:** htmx partial refreshes on a 10s tick + Chart.js for throughput/timeline charts. No SPA, no build step; all assets vendored, **zero external CDNs/fonts/scripts**.
- **Packaging:** Single static binary (`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`); web assets + GeoIP DB bundled via `go:embed`. CGO-free SQLite keeps the build glibc-free.
- **Metrics store:** `modernc.org/sqlite` (pure-Go) at `/var/lib/wireguard-dashboard`. Tables `system_metrics`, `traffic_metrics`, `client_traffic` (PK `ts,public_key`), `handshake_events` (`ts,public_key,name`); each `ts`-indexed.
- **Poller:** background sampler + hourly retention sweep (`PruneBefore`, ~8d horizon to back the 7d chart range) — no unbounded growth.
- **Geolocation:** embedded **DB-IP IP-to-City Lite** (GeoIP2/MMDB schema via `oschwald/geoip2-golang`) → country/city + lat/lon, fully offline. Migrated from GeoLite2 (006).
- **Views:** Server / Clients / Events tabs. Per-client expand panel = throughput chart + connection timeline (online/offline bands) + history summary (online/last-seen, session count, connected time). Offline world-map card (embedded SVG + equirectangular-projected markers) on the Clients tab (006).
- **Host data sources:** read-only `wg show wg0 dump`/`public-key` + `systemctl is-active/show wg-quick@wg0` via scoped NOPASSWD sudoers; client manifest `/etc/wireguard-dashboard/clients.json` (0640). **Never holds client private keys.**

---

## 7. Dashboard Build & Deployment

- **Distribution (spec 005):** public GitHub Release artifact from `vkatrichenko/wireguard-vpn`, pinned via `dashboard_release_tag` in `terraform/dev/main.tf` (currently `v0.0.3`) — single reviewable source of truth, same explicit-pin philosophy as the AMI. Replaced the earlier private S3-artifact + ECR path.
- **Install (cloud-init):** user-data downloads the binary + `SHA256SUMS` from the pinned release and verifies the checksum before install; provisioned only when `dashboard_release_tag` is non-empty.
- **Service:** systemd `wireguard-dashboard.service`, `Requires`/`After=wg-quick@wg0`; runs as a dedicated `wireguard-dashboard` system user (nologin).
- **Binding & access:** `LISTEN_ADDR=172.16.15.1:8080` — bound to the WireGuard tunnel IP, so reachable only over the VPN (no public listener; this is why no in-band auth is needed).
