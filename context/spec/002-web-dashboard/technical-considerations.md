# Technical Specification: Web Dashboard for WireGuard VPN

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

A self-contained **Next.js (App Router)** application running as a single Docker container on the existing WireGuard EC2 instance, fronted by an **AWS Application Load Balancer** that terminates HTTPS using an **ACM certificate** at `https://wg.vk.provectus.pro`. A new Route53 hosted zone `vk.provectus.pro` (delegated from the parent `provectus.pro`, which lives in another AWS account) provides DNS; the dashboard is exposed as the `wg` record within that zone. **AWS WAF v2** in front of the ALB protects the public Basic-auth endpoint with a rate-based rule. Metrics history (last 24 h) is persisted in a local **SQLite** file inside a Docker volume on the EC2.

```
Internet
   │
   ▼
Route53  (wg.vk.provectus.pro, A-alias)
   │
   ▼
WAF v2  (rate-based: 100 req / 5 min / IP)
   │
   ▼
ALB     (ACM cert; :443 → EC2:8080, :80 → 301 :443)
   │
   ▼
EC2 (existing WireGuard host)
   └─ Docker container: Next.js (port 8080)
        ├─ reads /host/proc, /host/sys
        ├─ exec wg show dump, systemctl status wg-quick@wg0
        ├─ writes/reads SQLite at /var/lib/wireguard-dashboard
        └─ Basic auth from SSM Parameter Store
```

Container builds run in **GitHub Actions on push to `main`** (paths-filter `dashboard/**`), publishing `linux/amd64` images to a private ECR repo via OIDC. A follow-on workflow triggers an SSM `SendCommand` to pull the new image and restart the systemd-managed `wireguard-dashboard` unit on the EC2.

---

## 2. Proposed Solution & Implementation Plan

### 2.1 Repository structure (additions and changes)

```
dashboard/                              # NEW — Next.js app, single folder
  app/
    api/
      health/route.ts                   # ALB health check, no auth
      snapshot/route.ts                 # aggregate payload (10s poll)
      metrics/route.ts                  # 24h time-series for charts
    page.tsx                            # dashboard UI
    layout.tsx
  middleware.ts                         # Basic auth gate
  lib/
    proc.ts                             # /host/proc + /host/sys readers
    wg.ts                               # `wg show wg0 dump` parser
    systemd.ts                          # systemctl wrappers
    db.ts                               # better-sqlite3 client
    clients-config.ts                   # reads /etc/wireguard-dashboard/clients.json
  instrumentation.ts                    # starts background poller
  components/                           # charts, cards, layout
  Dockerfile
  package.json
  next.config.ts
terraform/
  modules/
    dashboard/                          # NEW
      main.tf                           # ALB, target group, listener rules
      acm.tf                            # ACM cert + DNS validation
      dns.tf                            # Route53 zone + records
      ecr.tf                            # ECR repo + lifecycle policy
      waf.tf                            # WAF v2 web ACL + association
      sg.tf                             # ALB SG + companion rule for EC2
      iam.tf                            # GH OIDC role; instance role grants
      variables.tf, outputs.tf, versions.tf, locals.tf
    wireguard/                          # MODIFIED
      templates/user-data.txt           # add Docker install + container start
      sg.tf                             # add ingress 8080 from ALB SG
      variables.tf                      # new clients_config schema (with name)
      iam.tf                            # add ECR pull + dashboard SSM grants
  dev/
    main.tf                             # compose dashboard module, updated clients_config
    locals.tf                           # dashboard hostname + parent zone reference
.github/workflows/
  dashboard-build.yml                   # NEW — build + push on main
  dashboard-deploy.yml                  # NEW — SSM SendCommand to restart container
```

### 2.2 Architecture changes

- **Reuse the existing public subnets.** The VPC module already provisions three public subnets across three AZs; the ALB attaches to those — **no subnet work is required**.
- **New ALB** spanning the existing public subnets. Listeners: `:80` (redirect to HTTPS), `:443` (forward to EC2 target on `:8080`).
- **New ALB security group:** ingress `:443` and `:80` from `0.0.0.0/0`; egress `:8080` to the EC2 SG.
- **WireGuard EC2 SG:** new ingress rule for `:8080/tcp` sourced from the ALB SG. (UDP 51820 unchanged.)
- **New ECR private repo** `wireguard-dashboard`, with a lifecycle policy retaining the latest 10 untagged images and all `main-*` SHA tags for 30 days.
- **WAF v2 Web ACL** associated with the ALB. One rate-based rule: 100 req / 5 min per source IP, action `BLOCK`. CloudWatch metrics enabled.
- **New Route53 hosted zone** for `vk.provectus.pro`. NS delegation from parent `provectus.pro` is **out of band** — added manually in the parent account (one-time). The dashboard is exposed via an `A`-alias record `wg` in that zone, resolving to the ALB.
- **ACM certificate** for `wg.vk.provectus.pro` in `us-east-1`, DNS-validated against the new zone (validation records created by Terraform).
- **No CloudWatch agent** — system metrics read directly inside the container.
- **No second EC2** — container co-locates with WireGuard.

### 2.3 Data model — local SQLite schema

File: `/var/lib/wireguard-dashboard/metrics.db` (Docker volume `wireguard-dashboard-data`).

| Table | Purpose | Key columns |
| --- | --- | --- |
| `system_metrics` | CPU% and memory% over time | `ts INTEGER PRIMARY KEY`, `cpu_pct REAL`, `mem_pct REAL` |
| `traffic_metrics` | wg0 cumulative rx/tx bytes | `ts INTEGER PRIMARY KEY`, `rx_bytes_cum INTEGER`, `tx_bytes_cum INTEGER` |
| `client_traffic` | per-client cumulative rx/tx | `ts`, `public_key`, `name`, `address`, `rx_bytes_cum`, `tx_bytes_cum` (composite PK on `ts, public_key`) |
| `handshake_events` | detected handshakes (last hour) | `ts INTEGER`, `public_key`, `name` |

A background poller samples every 30 s; a maintenance task prunes rows older than 25 h every 10 minutes. Schemas managed via `better-sqlite3` migrations.

### 2.4 API contracts

All routes are Next.js App Router handlers under `dashboard/app/api/`. JSON in/out. All routes except `/api/health` require Basic auth (enforced by `middleware.ts`).

| Method | Path | Purpose / shape |
| --- | --- | --- |
| GET | `/api/health` | `{ ok: true }` — ALB target health check |
| GET | `/api/snapshot` | Aggregated payload: `{ system, traffic, clients[], service, server }` — single round-trip per 10s refresh |
| GET | `/api/metrics?range=24h` | Time-series arrays for sparklines/trend charts |

### 2.5 Backend module breakdown

- `lib/proc.ts` — reads `/host/proc/stat`, `/host/proc/meminfo`, `/host/sys/class/net/wg0/statistics/{rx_bytes,tx_bytes}`, `/host/proc/uptime`. Computes deltas for rates.
- `lib/wg.ts` — `child_process.execFile('/usr/bin/wg', ['show','wg0','dump'])`, parses the tab-separated output into peer rows (`{public_key, endpoint, allowed_ips, latest_handshake, rx, tx}`).
- `lib/systemd.ts` — `systemctl is-active wg-quick@wg0`, `systemctl show -p ActiveEnterTimestamp wg-quick@wg0` for service status and uptime.
- `lib/db.ts` — wraps `better-sqlite3` with prepared statements; exports query helpers per table.
- `lib/clients-config.ts` — reads `/etc/wireguard-dashboard/clients.json` (rendered by Terraform user-data) — the source of truth for `name → public_key → address`.
- `instrumentation.ts` — Next.js bootstrap hook; starts a 30 s setInterval that samples → writes to SQLite, plus a 10 min retention sweeper.

### 2.6 Frontend breakdown

- `app/page.tsx` — server component, initial render with first snapshot.
- `components/charts/Trend.tsx` — wrapper around **Recharts** `<AreaChart>` for CPU/memory/traffic 24h trends.
- `components/ClientList.tsx`, `ServiceStatusCard.tsx`, `ServerInfoCard.tsx`, `UptimeCard.tsx`.
- Client-side polling hook `usePoll('/api/snapshot', 10_000)` with stale-flag handling.

### 2.7 Container runtime requirements

`docker run` flags (encoded in the systemd unit installed by cloud-init):

- `--network host` — required so the container shares the host's net namespace and can see `wg0` for `wg show`.
- `--pid host` — so `systemctl` inside the container talks to the host's systemd via dbus.
- `--cap-add NET_ADMIN` — for `wg show` to read interface counters.
- Read-only bind mounts:
  - `/proc:/host/proc:ro`
  - `/sys:/host/sys:ro`
  - `/etc/wireguard:/etc/wireguard:ro`
  - `/etc/wireguard-dashboard/clients.json:/etc/wireguard-dashboard/clients.json:ro`
  - `/run/dbus/system_bus_socket:/run/dbus/system_bus_socket`
- Volume: `wireguard-dashboard-data:/var/lib/wireguard-dashboard`
- Env (resolved at unit start by an `ExecStartPre` that calls `aws ssm get-parameter`):
  - `BASIC_AUTH_USERNAME`
  - `BASIC_AUTH_PASSWORD_HASH` (bcrypt, verified per request)
  - `LISTEN_PORT=8080`

### 2.8 Cloud-init additions to `user-data.txt`

After the existing WireGuard install:

1. Install Docker (`apt install -y docker.io`).
2. Authenticate to ECR via the instance role (`aws ecr get-login-password | docker login`).
3. Render `/etc/wireguard-dashboard/clients.json` from a Terraform-templated value (the new `clients_config` shape).
4. Drop `/etc/systemd/system/wireguard-dashboard.service` (templated) and `systemctl enable --now wireguard-dashboard.service`.
5. Health-signal logic stays the same — ALB target health check is independent.

### 2.9 IAM additions

**Instance role** gains:
- `ecr:GetAuthorizationToken` (resource `*`)
- `ecr:BatchGetImage`, `ecr:GetDownloadUrlForLayer`, `ecr:BatchCheckLayerAvailability` on the dashboard ECR repo ARN
- `ssm:GetParameter` on:
  - `/config/wireguard/dashboard/username`
  - `/config/wireguard/dashboard/password-hash`

**New GitHub OIDC role** (federated from `token.actions.githubusercontent.com`, scoped to this repo):
- ECR push permissions on the dashboard repo
- `ssm:SendCommand` on the WireGuard instance ARN with a hard-pinned document

### 2.10 GitHub Actions workflows

`dashboard-build.yml`:
- Trigger: `push: branches: [main]`, paths: `dashboard/**`
- Steps: checkout → configure-aws-credentials (OIDC) → docker buildx build `--platform linux/amd64` → ECR login → push tags `main-${{ github.sha }}` and `latest`

`dashboard-deploy.yml`:
- Trigger: `workflow_run` after `dashboard-build.yml` succeeds; or manual `workflow_dispatch`
- Step: `aws ssm send-command` with a document that runs:
  ```
  docker pull <ECR>/wireguard-dashboard:main-<sha>
  systemctl restart wireguard-dashboard
  ```

### 2.11 `clients_config` Terraform schema migration

Today: `clients_config = [ { "172.16.15.5/32" = "publickey" } ]`

New: `clients_config = [ { name = "vkatrychenko", address = "172.16.15.5/32", public_key = "..." } ]`

This is a breaking change to the `wireguard` module input. The user-data template and `dev/main.tf` are updated in the same commit. The module README documents the migration.

---

## 3. Impact and Risk Analysis

### 3.1 System Dependencies

- **Parent zone `provectus.pro` lives outside this AWS account.** NS delegation for `vk.provectus.pro` is a one-time manual edit there. Without it, ACM DNS validation never completes. **This must happen before `terraform apply`.**
- **EC2 has `lifecycle.ignore_changes = [user_data, user_data_base64]`.** Cloud-init changes don't replace the instance; the operator must `terraform taint` it (or `terraform apply -replace=...`) to pick up new bootstrap logic.
- **SSM Parameter Store credentials** for the dashboard must be created out-of-band (same pattern as the WireGuard private key).

### 3.2 Potential Risks & Mitigations

| Risk | Likelihood | Mitigation |
| --- | --- | --- |
| Brute-force on public Basic auth | Medium | WAF rate-based rule (100 req / 5 min / IP) + ≥ 20-char password. CloudWatch metric filter on ALB access logs alarms on 401 spikes (out-of-scope to wire up here). Resolves the open `[NEEDS CLARIFICATION]` from §2.7 of the functional spec. |
| Container can't see host system metrics | Medium | Documented `--network host` + `--pid host` + bind mounts of `/host/proc`, `/host/sys`. Verified locally before merge. |
| Memory pressure on `t3a.micro` (1 GB) | Medium | Next.js (production) baseline ~150–200 MB; dashboard budget ≤ 350 MB total. If the 30-day p95 exceeds budget, follow-up task to upsize to `t3a.small`. |
| ACM validation race (parent NS not delegated) | Low (manual, one-shot) | Deploy guide makes the manual NS-records step explicit prior to `terraform apply`. |
| ECR pull fails on EC2 boot | Low | Cloud-init logs to `/var/log/cloud-init-output.log`. Systemd unit's `Restart=on-failure` retries. |
| Public exposure of dashboard | High by design | Mitigated by Basic auth + WAF rate rule. WAF IP-set allow-list can be added later if needed. |
| ALB cost (~$16/mo + LCU) | Accepted | Documented; chosen over Caddy/Let's Encrypt to honor the ACM decision. |
| `clients_config` schema break | Medium | Coordinated PR: schema change + user-data + `dev/main.tf` in one commit. `terraform plan` reviewed for unintended replacements. |
| SSM `SendCommand` deploy has no rollback | Medium | Operator can manually `docker run` a previous tag. Blue/green deployment is out of scope for v1. |
| Charts library (Recharts) bundle size on mobile | Low | Recharts gzipped ~50–60 KB. Acceptable for a dashboard. Lazy-loaded on the chart route segment if needed. |

---

## 4. Testing Strategy

- **Local development:** `docker compose` running the Next.js container with mock `/host/proc` and `/host/sys` files; develop against `http://localhost:8080`.
- **Backend unit tests (Vitest):** `lib/proc.ts`, `lib/wg.ts`, `lib/systemd.ts` — mock filesystem and `child_process`. Cover edge cases: no peers, never-handshaked peer, kernel counter wraps, `wg-quick@wg0` stopped.
- **Frontend component tests (React Testing Library):** chart rendering, `ClientList` empty/populated states, `ServiceStatusCard` running/stopped variants.
- **Integration test:** A `make` target that builds the image, runs it with synthetic env, and asserts `/api/health` returns 200 and `/api/snapshot` returns valid JSON when authenticated.
- **Manual end-to-end:** After `terraform apply`, open `https://wg.vk.provectus.pro` and verify all six widgets render with non-empty data; SSM-exec `systemctl stop wg-quick@wg0` and confirm the UI shows **"Service down"** within one refresh cycle (~10 s).
- **Terraform validation:** `terraform validate` in `terraform/dev/`, `terraform/dev/backend/`, and the new `terraform/modules/dashboard/`. `make pre-commit` (fmt + tflint + trivy + docs) at repo root before claiming done.
