# Technical Specification: Web Dashboard for WireGuard VPN

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Completed (v3 — Go binary, VPN-only access)
- **Author(s):** Vladyslav Katrychenko

> **v3 note (2026-05-01):** Second architecture pivot. v1 used Next.js + Docker + ECR + ALB + ACM + Route53 + WAF. v2 swapped Next.js for a Go single binary delivered via S3, kept the ALB+ACM+Route53+WAF edge. v3 drops the public edge entirely: the dashboard listens on the WireGuard server's tunnel IP and is reachable **only by clients connected to the VPN**. HTTP is acceptable inside the WG tunnel (the tunnel itself is encrypted with ChaCha20-Poly1305). Basic auth is retained as defense-in-depth. No domain, no ACM, no Route53, no ALB, no WAF.

---

## 1. High-Level Technical Approach

A self-contained **Go single static binary** running as a systemd-managed process on the existing WireGuard EC2 instance, **bound to the WireGuard server's tunnel IP** (`172.16.15.1:8080`). The dashboard is reachable **only over the WG tunnel** — any client already connected to the VPN can hit `http://172.16.15.1:8080` and authenticate with HTTP Basic auth. The public internet cannot reach the dashboard at all (the EC2 security group does not expose port 8080). Metrics history (last 24 h) is persisted in a local **SQLite** file.

```
WireGuard client (e.g. operator's laptop)
   │
   │ encrypted WG tunnel (UDP 51820, ChaCha20-Poly1305)
   ▼
EC2 (existing WireGuard host, t3a.micro, Ubuntu 24.04)
   ├─ wg-quick@wg0.service             (existing — listens on 0.0.0.0:51820/udp)
   │     └─ wg0 interface              (172.16.15.1/24)
   └─ wireguard-dashboard.service      (NEW — Go binary, binds 172.16.15.1:8080)
        ├─ reads /proc, /sys directly
        ├─ exec sudo wg show wg0 dump, sudo systemctl status wg-quick@wg0
        ├─ writes/reads SQLite at /var/lib/wireguard-dashboard
        └─ Basic auth from SSM Parameter Store (defense-in-depth inside the tunnel)
```

**Build & deploy pipeline:**

```
Developer push to main (touching dashboard/**)
   │
   ▼
GitHub Actions (OIDC → AWS)
   ├─ go test ./... && go build -ldflags "-s -w" → 15-25 MB static binary
   └─ aws s3 cp wireguard-dashboard s3://<artifacts-bucket>/main-<sha>/wireguard-dashboard
       and aws s3 cp wireguard-dashboard s3://<artifacts-bucket>/latest/wireguard-dashboard
   │
   ▼
Optional follow-on workflow → SSM SendCommand
   ├─ aws s3 cp s3://.../main-<sha>/wireguard-dashboard /opt/wireguard-dashboard/bin/wireguard-dashboard.new
   ├─ chmod +x; atomic mv into place
   └─ systemctl restart wireguard-dashboard
```

**Why VPN-only access works here:**

- The user is already a WG operator and is the *only* persona who needs the dashboard. They're connected to the VPN by definition.
- No ALB ($16/mo), no WAF (~$5/mo), no Route53 zone (parent-zone delegation problem disappears), no ACM cert lifecycle, no public attack surface for credential brute-forcing.
- Encryption is provided by WG itself; HTTP is appropriate inside the tunnel.
- Failure mode: if WG service is down, the dashboard is naturally unreachable — but if WG is down, the dashboard's most important reading is "WG service is down", which the operator already knows because their client can't reach it. Acceptable.

---

## 2. Proposed Solution & Implementation Plan

### 2.1 Repository structure (additions and changes)

```
dashboard/                              # NEW — Go project, single folder
  cmd/
    wireguard-dashboard/
      main.go                           # entrypoint, flag parsing, server start
  internal/
    server/                             # HTTP routing, middleware, route handlers
    proc/                               # /proc + /sys readers
    wg/                                 # `wg show wg0 dump` parser
    systemd/                            # systemctl wrappers
    db/                                 # SQLite layer (modernc.org/sqlite — pure-Go, no CGO)
    poller/                             # 30s sampler + retention sweeper
    config/                             # env-var loading
    clientsfile/                        # reads /etc/wireguard-dashboard/clients.json
  web/                                  # served via embed.FS
    templates/                          # layout, dashboard, cards/
    static/                             # htmx.min.js, chart.umd.min.js, app.css
  go.mod
  go.sum
  Makefile
terraform/
  modules/
    dashboard/                          # NEW — only an artifact bucket, no edge
      s3.tf                             # binary artifact bucket + lifecycle
      iam.tf                            # GH OIDC role for S3 push and SSM SendCommand
      versions.tf, variables.tf, outputs.tf, locals.tf
    wireguard/                          # MODIFIED
      templates/user-data.txt           # add binary download + sudoers + systemd unit
      variables.tf                      # new clients_config schema (with name)
      iam.tf                            # add S3 read grant + SSM Parameter Store grants
      # sg.tf — UNCHANGED (no new ingress rules)
  dev/
    main.tf                             # compose dashboard module, updated clients_config
    locals.tf                           # artifact bucket name
.github/workflows/
  dashboard-build.yml                   # NEW — go build + S3 upload on main
  dashboard-deploy.yml                  # NEW — SSM SendCommand to download + restart
```

### 2.2 Architecture changes

- **No ALB. No ACM. No Route53. No WAF.** All four removed from the design.
- **No new EC2 ingress for the dashboard.** The WireGuard EC2 SG keeps its existing rules: UDP 51820 from `0.0.0.0/0`, and whatever SSH/SSM access already exists. Port 8080 is **not** opened to the internet.
- **New S3 bucket** for dashboard binary artifacts (e.g., `wireguard-vpn-test-dashboard-artifacts`). Bucket policy permits writes only from the GitHub OIDC role and reads only from the EC2 instance role. Lifecycle: keep last 30 versions of `latest/wireguard-dashboard`, expire `main-*/` prefixes after 60 days.
- **Dashboard listens on `172.16.15.1:8080`** (the WG server's tunnel IP, derived from `var.wg_server_net` minus the CIDR — currently `172.16.15.1/24` so the host IP is `172.16.15.1`). The Go binary's `LISTEN_ADDR` env defaults to that value. Configurable via env so that local dev can use `127.0.0.1:8080`.
- **Systemd ordering:** the dashboard unit declares `After=wg-quick@wg0.service` and `Requires=wg-quick@wg0.service`, so it only starts once `wg0` is up (and therefore `172.16.15.1` is bindable). When WG is restarted, the dashboard restarts too.

### 2.3 Data model — local SQLite schema

File: `/var/lib/wireguard-dashboard/metrics.db`. Owned by the `wireguard-dashboard` system user (created by cloud-init).

| Table | Purpose | Key columns |
| --- | --- | --- |
| `system_metrics` | CPU% and memory% over time | `ts INTEGER PRIMARY KEY`, `cpu_pct REAL`, `mem_pct REAL` |
| `traffic_metrics` | wg0 cumulative rx/tx bytes | `ts INTEGER PRIMARY KEY`, `rx_bytes_cum INTEGER`, `tx_bytes_cum INTEGER` |
| `client_traffic` | per-client cumulative rx/tx | `ts`, `public_key`, `name`, `address`, `rx_bytes_cum`, `tx_bytes_cum` (composite PK on `ts, public_key`) |
| `handshake_events` | detected handshakes (last hour) | `ts INTEGER`, `public_key`, `name` |

Background poller samples every 30 s; retention task prunes rows older than 25 h every 10 minutes. Driver: **`modernc.org/sqlite`** (pure-Go, no CGO).

### 2.4 API contracts

All HTTP routes are served by the Go binary's `net/http` mux (or `chi`). All routes except `/api/health` require Basic auth.

| Method | Path | Purpose / shape |
| --- | --- | --- |
| GET | `/api/health` | `{ "ok": true }` — used by `systemctl` health-checks and a curl sanity test |
| GET | `/api/snapshot` | Aggregated payload: `{ system, traffic, clients[], service, server }` — single round-trip per 10s refresh |
| GET | `/api/metrics?range=24h` | Time-series arrays for sparklines/trend charts |
| GET | `/` | Server-rendered HTML dashboard (htmx-enabled) |

### 2.5 Backend module breakdown (Go)

- `cmd/wireguard-dashboard/main.go` — flags + env parsing, constructs server, blocks on `http.ListenAndServe`. Graceful shutdown on SIGTERM.
- `internal/config/` — loads `LISTEN_ADDR` (default `172.16.15.1:8080`), `BASIC_AUTH_USERNAME`, `BASIC_AUTH_PASSWORD_HASH`, `CLIENTS_CONFIG_PATH`, `DB_PATH` from env.
- `internal/server/` — HTTP routing. Likely `github.com/go-chi/chi/v5` for middleware composition; plain `net/http` is also viable. Embeds `web/templates` and `web/static` via `embed.FS`.
- `internal/server/middleware_auth.go` — Basic auth using `golang.org/x/crypto/bcrypt`. Skips `/api/health`.
- `internal/proc/` — reads `/proc/stat`, `/proc/meminfo`, `/sys/class/net/wg0/statistics/{rx,tx}_bytes`, `/proc/uptime`. Computes CPU% and rates from prior in-memory sample.
- `internal/wg/` — `os/exec` runs `sudo /usr/bin/wg show wg0 dump`, parses tab-separated output.
- `internal/systemd/` — runs `sudo /usr/bin/systemctl is-active wg-quick@wg0`, `sudo /usr/bin/systemctl show -p ActiveEnterTimestamp wg-quick@wg0`.
- `internal/db/` — `modernc.org/sqlite` with prepared statements; query helpers per table.
- `internal/poller/` — goroutines with `time.Ticker(30s)` for sampling and `Ticker(10m)` for retention.
- `internal/clientsfile/` — reads `/etc/wireguard-dashboard/clients.json`.

### 2.6 Frontend (server-rendered + htmx + Chart.js)

- `web/templates/layout.html` — base HTML, includes htmx + Chart.js + app.css.
- `web/templates/dashboard.html` — composes the cards into the page grid.
- `web/templates/cards/*.html` — one template per widget (client-list, server-info, service-status, system, network-rate, events, charts). Each card uses `hx-get="/api/snapshot"`, `hx-trigger="every 10s"`, `hx-target="this"` for autonomous 10 s refresh; the API returns HTML fragments.
- `/api/metrics?range=24h` returns JSON consumed by Chart.js drawing into `<canvas>` elements. Re-render triggered client-side via `htmx:afterSettle`.
- htmx and Chart.js are vendored as pinned files in `web/static/`.

### 2.7 Runtime requirements

The Go binary runs as a non-root **`wireguard-dashboard`** system user (created by cloud-init via `useradd --system --no-create-home --shell /usr/sbin/nologin`). Permissions:

- Read access to `/proc`, `/sys`.
- Read access to `/etc/wireguard-dashboard/clients.json` (rendered with mode 0644 by cloud-init).
- Permission to exec `wg` and `systemctl` for a fixed set of read-only commands via `/etc/sudoers.d/wireguard-dashboard`:
  - `wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 dump`
  - `wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg show wg0 public-key`
  - `wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl is-active wg-quick@wg0.service`
  - `wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/systemctl show wg-quick@wg0.service*`
  - File mode `0440`, owner `root:root`.
- Write access to `/var/lib/wireguard-dashboard/` for the SQLite file.

Environment variables (resolved at unit start by an `ExecStartPre` that calls `aws ssm get-parameter`):
- `LISTEN_ADDR=172.16.15.1:8080`
- `BASIC_AUTH_USERNAME`
- `BASIC_AUTH_PASSWORD_HASH` (bcrypt)
- `CLIENTS_CONFIG_PATH=/etc/wireguard-dashboard/clients.json`
- `DB_PATH=/var/lib/wireguard-dashboard/metrics.db`

### 2.8 Cloud-init additions to `user-data.txt`

After the existing WireGuard install:

1. `useradd --system --no-create-home --shell /usr/sbin/nologin wireguard-dashboard` (idempotent guard).
2. `mkdir -p /opt/wireguard-dashboard/bin /var/lib/wireguard-dashboard /etc/wireguard-dashboard`; chown `wireguard-dashboard:wireguard-dashboard /var/lib/wireguard-dashboard`.
3. Render `/etc/wireguard-dashboard/clients.json` from a Terraform-templated value.
4. `aws s3 cp s3://<artifact-bucket>/latest/wireguard-dashboard /opt/wireguard-dashboard/bin/wireguard-dashboard`.
5. `chmod +x /opt/wireguard-dashboard/bin/wireguard-dashboard`.
6. Drop `/etc/sudoers.d/wireguard-dashboard` (mode 0440) granting NOPASSWD for the four scoped commands.
7. Drop `/etc/systemd/system/wireguard-dashboard.service` with `After=wg-quick@wg0.service`, `Requires=wg-quick@wg0.service`. `systemctl enable --now`.

### 2.9 IAM additions

**Instance role** gains:
- `s3:GetObject` on `arn:aws:s3:::<artifact-bucket>/latest/*` and `arn:aws:s3:::<artifact-bucket>/main-*/*`
- `s3:ListBucket` on the artifact bucket (with key-prefix conditions for `latest/` and `main-*/`)
- `ssm:GetParameter` on:
  - `/config/wireguard/dashboard/username`
  - `/config/wireguard/dashboard/password-hash`

**New GitHub OIDC role** (federated from `token.actions.githubusercontent.com`, scoped to this repository):
- `s3:PutObject` on the artifact bucket
- `ssm:SendCommand` on the WireGuard instance ARN with a hard-pinned document

**No ECR, ALB, Route53, ACM, or WAF permissions anywhere.**

### 2.10 GitHub Actions workflows

`dashboard-build.yml`:
- Trigger: `push: branches: [main]`, paths: `dashboard/**`
- Steps: checkout → set up Go (`actions/setup-go@v5` pinned by SHA, version pinned in `go.mod`) → `cd dashboard && go test ./...` → `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o wireguard-dashboard ./cmd/wireguard-dashboard` → configure-aws-credentials (OIDC) → `aws s3 cp` to `main-<sha>/` and `latest/`.

`dashboard-deploy.yml`:
- Trigger: `workflow_run` after `dashboard-build.yml` succeeds; or manual `workflow_dispatch`
- Step: `aws ssm send-command` with a hard-pinned document that:
  ```
  aws s3 cp s3://<bucket>/main-<sha>/wireguard-dashboard /opt/wireguard-dashboard/bin/wireguard-dashboard.new
  chmod +x /opt/wireguard-dashboard/bin/wireguard-dashboard.new
  mv /opt/wireguard-dashboard/bin/wireguard-dashboard.new /opt/wireguard-dashboard/bin/wireguard-dashboard
  systemctl restart wireguard-dashboard
  ```

### 2.11 `clients_config` Terraform schema migration

Today: `clients_config = [ { "172.16.15.5/32" = "publickey" } ]`

New: `clients_config = [ { name = "vkatrychenko", address = "172.16.15.5/32", public_key = "..." } ]`

Same as v1/v2 — unchanged by the architecture pivot.

---

## 3. Impact and Risk Analysis

### 3.1 System Dependencies

- **EC2 has `lifecycle.ignore_changes = [user_data, user_data_base64]`.** Cloud-init changes don't replace the instance; the operator must `terraform apply -replace=...` to pick up new bootstrap logic.
- **SSM Parameter Store credentials** for the dashboard must be created out-of-band (same pattern as the WireGuard private key).
- **Existing committed Next.js scaffold under `dashboard/`** must be removed before the Go project is added.
- **Dashboard depends on `wg-quick@wg0.service`** for binding (the WG IP only exists when WG is up). `systemd` ordering handles this.

### 3.2 Potential Risks & Mitigations

| Risk | Likelihood | Mitigation |
| --- | --- | --- |
| Operator can't reach dashboard when WG service is down | Certain by design | Acceptable — when WG is down, the dashboard's primary value (showing WG state) is moot. Operator falls back to SSM Session Manager. |
| Dashboard binds before `wg0` exists | Low | systemd `After=wg-quick@wg0.service` + `Requires=wg-quick@wg0.service` plus `Restart=on-failure RestartSec=5s` covers transient races. |
| Privileged commands (`wg`, `systemctl`) misused | Low | Sudoers entry scoped to four exact arg patterns, file mode 0440. |
| Memory pressure on `t3a.micro` (1 GB) | Low | Go binary baseline ~30 MB RAM. |
| S3 artifact pull fails on EC2 boot | Low | Cloud-init logs to `/var/log/cloud-init-output.log`. Systemd unit's `Restart=on-failure` retries. |
| Basic auth credentials on the wire (HTTP inside WG tunnel) | Low | WG tunnel encrypts the entire path. HTTP is acceptable inside it. Basic auth is defense-in-depth in case multiple WG clients share the tunnel. |
| Operator has multiple WG client devices | Acceptable | Each WG client can reach the dashboard. Basic auth is the gate; password sharing across the operator's own devices is the operator's choice. |
| `clients_config` schema break | Medium | Coordinated PR: schema change + user-data + `dev/main.tf` in one commit. `terraform plan` reviewed for unintended replacements. |
| Existing committed Next.js / ECR / dashboard work in the tree | Medium | Slice 0 explicitly removes `dashboard/` (Next.js), `terraform/modules/ecr/`, and the ECR-related vars/iam/locals/user-data blocks before Go work begins. |
| Chart.js / htmx CDN drift | Low | Both vendored as pinned files in `web/static/`, committed to the repo. |

---

## 4. Testing Strategy

- **Local development:** `cd dashboard && make run` builds and runs the binary against synthetic env (`make run-with-fakeproc` — points at fixture files in `testdata/`). Develops against `http://127.0.0.1:8080` (override `LISTEN_ADDR`).
- **Backend unit tests (`go test ./...`):** table-driven tests for `internal/proc`, `internal/wg`, `internal/systemd`, `internal/db` (in-memory SQLite via `:memory:`), `internal/server` (httptest). Cover edge cases: no peers, never-handshaked peer, kernel counter wraps, `wg-quick@wg0` stopped.
- **Frontend smoke:** `httptest` against `/` to verify the dashboard HTML includes each card's expected content; no JS-runtime tests in v1.
- **Integration test:** `make` target builds the binary, runs it with synthetic env, and curls `/api/health` and `/api/snapshot`.
- **Manual end-to-end:** After `terraform apply` and a CI-driven binary upload + deploy, **connect to the VPN**, then open `http://172.16.15.1:8080` in browser and verify all six widgets render. SSM-exec `systemctl stop wg-quick@wg0` (from outside the tunnel) and confirm the dashboard becomes unreachable (expected — the WG IP goes down).
- **Terraform validation:** `terraform validate` in `terraform/dev/` and the new `terraform/modules/dashboard/`. `make pre-commit` (fmt + tflint + trivy + docs) at repo root before claiming done.
