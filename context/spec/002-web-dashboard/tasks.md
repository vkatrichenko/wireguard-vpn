# Tasks: Web Dashboard for WireGuard VPN

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Technical Specification:** [`technical-considerations.md`](./technical-considerations.md) (v3 — Go binary, VPN-only access)

> **v3 note (2026-05-01):** Architecture pivoted twice. Final shape: a Go single static binary on the existing WireGuard EC2, **bound to the WG tunnel IP `172.16.15.1:8080` and reachable only over the VPN**. No domain, ACM, Route53, ALB, or WAF. Basic auth retained as defense-in-depth inside the tunnel. Slice 0 reverts the v1 (Next.js + Docker + ECR) work that has already landed; Slices 1–13 build the Go path.

Vertical slices — each leaves the system runnable with new verifiable value.

---

## Slice 0: Revert v1 (Next.js + Docker + ECR) deliverables

**Outcome:** The working tree no longer carries dead Next.js / Docker / ECR code. WireGuard EC2 still applies cleanly with no dashboard provisioning.

- [x] Delete `dashboard/` (Next.js scaffold + Dockerfile + package.json + tsconfig + node_modules artifacts). **[Agent: go-fullstack]**
- [x] Delete `terraform/modules/ecr/` (the off-spec generic ECR module). **[Agent: terraform-aws]**
- [x] Revert `terraform/dev/main.tf` — remove the `module "ecr"` block and the `dashboard_ecr_repository_arn` arg passed to `module "wireguard"`. **[Agent: terraform-aws]**
- [x] Revert `terraform/dev/locals.tf` — remove `local.ecr_repositories`. **[Agent: terraform-aws]**
- [x] Revert `terraform/modules/wireguard/variables.tf` — remove `dashboard_ecr_repository_arn` and `dashboard_image_uri` variables. **[Agent: terraform-aws]**
- [x] Revert `terraform/modules/wireguard/iam.tf` — remove the two ECR `dynamic "statement"` blocks. **[Agent: terraform-aws]**
- [x] Revert `terraform/modules/wireguard/locals.tf` — remove `dashboard_image_uri` from the `templatefile(...)` map. **[Agent: terraform-aws]**
- [x] Revert `terraform/modules/wireguard/templates/user-data.txt` — remove the `%{ if dashboard_image_uri != "" ~} … %{ endif ~}` Docker block, restore the original `exit 0` / `exit 1` in the WireGuard health-signal loop. **[Agent: linux-cloud-init]**
- [x] Run `make pre-commit` — confirm clean state, no orphan vars, no dangling references. **[Agent: terraform-aws]**

---

## Slice 1: Hello-world Go binary listening on the WG IP

**Outcome:** A Go binary is running as `wireguard-dashboard.service` on the EC2, bound to `172.16.15.1:8080`. Connect to the VPN, then `curl http://172.16.15.1:8080/api/health` returns `{"ok":true}` and `http://172.16.15.1:8080/` shows a placeholder page. Public internet cannot reach it (no SG rule for 8080).

- [x] Scaffold `dashboard/` Go module — `go.mod` (Go pinned to current stable, e.g. `go 1.23`), `cmd/wireguard-dashboard/main.go`, `internal/server/server.go` with two handlers: `GET /api/health` returning `{"ok":true}` and `GET /` returning a server-rendered "WireGuard Dashboard" page. Embed templates via `embed.FS`. `LISTEN_ADDR` env (default `172.16.15.1:8080`, overridable for local dev). `Makefile` with `build`, `run`, `test` targets. **[Agent: go-fullstack]**
- [x] TF: new `terraform/modules/dashboard/` — `s3.tf` (private artifact bucket `<project>-dashboard-artifacts`, versioning on, public-access fully blocked, lifecycle: keep last 30 versions of `latest/`, expire `main-*/` after 60 days) plus `versions.tf`, `variables.tf`, `locals.tf`, `outputs.tf` mirroring the wireguard module's style. **[Agent: terraform-aws]**
- [x] TF: extend `terraform/modules/wireguard/iam.tf` with `s3:GetObject` on the artifact bucket (`latest/*` and `main-*/*`) plus `s3:ListBucket` with key-prefix conditions. New module input variable `dashboard_artifact_bucket_arn`. **[Agent: terraform-aws]**
- [x] Update `terraform/modules/wireguard/templates/user-data.txt` — gated on a new `dashboard_artifact_bucket_name` template variable, append a block that: creates the `wireguard-dashboard` system user, makes `/opt/wireguard-dashboard/bin`, `/var/lib/wireguard-dashboard`, downloads `s3://<bucket>/latest/wireguard-dashboard`, chmod +x, drops `/etc/systemd/system/wireguard-dashboard.service` (`After=wg-quick@wg0.service Requires=wg-quick@wg0.service`, `ExecStart=/opt/wireguard-dashboard/bin/wireguard-dashboard`, `User=wireguard-dashboard`, `Restart=on-failure`, `Environment=LISTEN_ADDR=172.16.15.1:8080`), `systemctl enable --now`. All bash variables escaped `$$VAR`. **[Agent: linux-cloud-init]**
- [x] Compose the new dashboard module from `terraform/dev/main.tf`; pass artifact-bucket ARN/name through to `module "wireguard"`. **[Agent: terraform-aws]**
- [x] Build + upload binary manually from laptop: `cd dashboard && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w" -o wireguard-dashboard ./cmd/wireguard-dashboard && AWS_PROFILE=csm aws s3 cp wireguard-dashboard s3://<bucket>/latest/wireguard-dashboard`. **[Agent: go-fullstack]** _(Build done; upload deferred to post-apply per chosen "single apply, then upload, then SSM-restart" orchestration.)_
- [x] `terraform apply` and `terraform apply -replace=module.wireguard.aws_instance.wireguard` to pick up new user-data. **[Agent: terraform-aws]**
- [x] Verify: connect to VPN, `curl http://172.16.15.1:8080/api/health` returns `{"ok":true}`, browser to `http://172.16.15.1:8080/` shows the page. **From the public internet** (without VPN) the URL must be unreachable — confirm with `curl --max-time 5 http://<ec2-public-ip>:8080/api/health` timing out. **[Agent: go-fullstack]**

---

## Slice 2: HTTP Basic auth gate — **SKIPPED**

**Decision (2026-05-04):** De-scoped. VPN client membership is the access gate — a client whose `AllowedIPs` doesn't include `172.16.15.1` cannot reach the dashboard at all. For the solo-operator persona, that's sufficient access control. Basic auth would be redundant defense-in-depth at the cost of SSM-param management, bcrypt wiring, and another env-var dance. The functional spec's §2.7 (Authentication) and the technical spec's middleware/SSM/IAM mentions of Basic auth should be revised in a follow-up `/awos:tech` pass to reflect the new "no in-band auth, gate via WG client config" model.

- [x] ~~Manual: create SSM SecureString params `/config/wireguard/dashboard/username` and `/config/wireguard/dashboard/password-hash` (≥ 20-char password, bcrypt-hashed).~~ _Skipped — no Basic auth._
- [x] ~~TF: extend EC2 IAM with `ssm:GetParameter` on those two paths.~~ _Skipped — no Basic auth._
- [x] ~~Update systemd unit to add `ExecStartPre` that fetches creds via `aws ssm get-parameter` and writes them into `EnvironmentFile=/etc/wireguard-dashboard/auth.env` (mode 0640, owner `root:wireguard-dashboard`).~~ _Skipped — no Basic auth._
- [x] ~~Implement `internal/server/middleware_auth.go` — Basic-auth middleware using `golang.org/x/crypto/bcrypt`, exempt `/api/health`.~~ _Skipped — no Basic auth._
- [x] ~~Go unit test for middleware (correct, wrong, missing header).~~ _Skipped — no Basic auth._
- [x] ~~Build + upload binary; SSM-exec download + restart.~~ _Skipped — no Basic auth._
- [x] ~~Verify: VPN client browser → 401 prompt; correct creds → page renders.~~ _Skipped — no Basic auth._

---

## Slice 3: `clients_config` schema migration to `{ name, address, public_key }`

**Outcome:** Same VPN connectivity as before, but Terraform now passes named clients and renders `/etc/wireguard-dashboard/clients.json` for the dashboard to consume.

- [x] Update `terraform/modules/wireguard/variables.tf` — `clients_config` to `list(object({ name, address, public_key }))`. **[Agent: terraform-aws]**
- [x] Update `terraform/modules/wireguard/templates/user-data.txt` peers section to consume the new shape; add a step that writes `/etc/wireguard-dashboard/clients.json` from the templated value. **[Agent: linux-cloud-init]**
- [x] Update `terraform/dev/main.tf` to pass `[{ name = "vkatrychenko", address = "172.16.15.5/32", public_key = "..." }]`. **[Agent: terraform-aws]**
- [x] Run `terraform plan -out=tfplan` and inspect: only EC2 user-data + clients.json should change. **[Agent: terraform-aws]**
- [x] Apply + replace EC2; verify `/etc/wireguard-dashboard/clients.json` content via SSM session; existing VPN client still connects. **[Agent: linux-cloud-init]**

---

## Slice 4: Server endpoint info card

**Outcome:** Logged-in user sees public IP, port 51820, and server public key (with copy-to-clipboard).

- [x] `internal/serverinfo/serverinfo.go` — IMDSv2 for public IP; `sudo wg show wg0 public-key` for server pubkey. **[Agent: go-fullstack]**
- [x] Drop `/etc/sudoers.d/wireguard-dashboard` (mode 0440) granting NOPASSWD for the four scoped commands (cloud-init step). **[Agent: linux-cloud-init]**
- [x] `GET /api/server` handler. **[Agent: go-fullstack]**
- [x] `web/templates/cards/server-info.html` with copy-to-clipboard JS snippet. **[Agent: go-fullstack]**
- [x] Wire into `web/templates/dashboard.html`. **[Agent: go-fullstack]**
- [x] Go test (mocked exec). **[Agent: go-fullstack]**
- [x] Build + upload + verify in browser. **[Agent: go-fullstack]**

---

## Slice 5: WireGuard service status + uptime card

**Outcome:** Card shows Running/Stopped + service uptime; goes red within ~10s when service stopped (note: stopping `wg-quick@wg0` will also kill the dashboard's binding — verification done via brief simulated shutdown).

- [x] `internal/systemd/systemd.go` — `sudo systemctl is-active wg-quick@wg0`, `sudo systemctl show -p ActiveEnterTimestamp wg-quick@wg0`. **[Agent: go-fullstack]**
- [x] `GET /api/service` handler. **[Agent: go-fullstack]**
- [x] `web/templates/cards/service-status.html` + `uptime.html`. **[Agent: go-fullstack]**
- [x] Go tests (running, stopped, never-started). **[Agent: go-fullstack]**

---

## Slice 6: Client list with online/offline + last handshake

**Outcome:** Table of configured peers with name, IP, status pill, last-handshake "X min ago".

- [x] `internal/clientsfile/clientsfile.go` — read `/etc/wireguard-dashboard/clients.json`. **[Agent: go-fullstack]**
- [x] `internal/wg/wg.go` — `sudo wg show wg0 dump` parser. **[Agent: go-fullstack]**
- [x] `GET /api/clients` handler joining wg-show output with clients.json. **[Agent: go-fullstack]**
- [x] `web/templates/cards/client-list.html` (empty state included). **[Agent: go-fullstack]**
- [x] Go tests for `internal/wg` (no peers / never-handshaked / normal). **[Agent: go-fullstack]**

---

## Slice 7: Per-client cumulative traffic in client list

**Outcome:** Each row shows bytes sent / received, updates after WG transfer.

- [x] Extend `internal/wg/wg.go` to expose rx/tx fields. **[Agent: go-fullstack]** _(done as part of Slice 6 sub-task 2 — `Peer.TransferRx` / `TransferTx` already exposed)_
- [x] Extend client-list template with humanized bytes columns. **[Agent: go-fullstack]** _(done as part of Slice 6 sub-task 4 — `humanBytes` helper + Sent/Received columns already in `client-list.html`)_
- [x] Test for the new column. **[Agent: go-fullstack]** _(covered by Slice 6 sub-task 5 — `TestServiceShow_ActivePeer` and `_NeverHandshaked` exercise rx/tx parsing)_

---

## Slice 8: Live system snapshot — CPU + memory + network rate

**Outcome:** Three live cards with current values (no charts yet).

- [x] `internal/proc/proc.go` — read `/proc/stat`, `/proc/meminfo`, `/sys/class/net/wg0/statistics/{rx,tx}_bytes`, `/proc/uptime`. Compute CPU% + rates from prior in-memory sample. **[Agent: go-fullstack]**
- [x] `GET /api/snapshot` handler — aggregate `{ system, traffic, clients, service, server }`. **[Agent: go-fullstack]**
- [x] `web/templates/cards/system.html` + `network-rate.html`. **[Agent: go-fullstack]**
- [x] Go test for `internal/proc` parsing. **[Agent: go-fullstack]**

---

## Slice 9: 24-hour history (SQLite poller + Chart.js trend charts)

**Outcome:** Sparkline charts populate over time; persist across binary restart.

- [x] `internal/db/db.go` (`modernc.org/sqlite`) — bootstrap `system_metrics`, `traffic_metrics`, `client_traffic` tables. **[Agent: go-fullstack]**
- [x] `internal/poller/poller.go` — 30s sampler + 10-min retention sweep (>25h). **[Agent: go-fullstack]**
- [x] `GET /api/metrics?range=24h` handler. **[Agent: go-fullstack]**
- [x] Vendor `web/static/chart.umd.min.js` (pinned Chart.js version, integrity-checked). **[Agent: go-fullstack]**
- [x] Trend chart partials in `web/templates/cards/charts/` for CPU, memory, rx, tx. **[Agent: go-fullstack]**
- [x] Go test for retention sweeper boundary. **[Agent: go-fullstack]**

---

## Slice 10: Recent handshake events (last hour)

**Outcome:** Card lists handshake events with timestamp + client name; updates within ~30s.

- [x] Add `handshake_events` table. **[Agent: go-fullstack]**
- [x] Extend poller to detect handshake-time changes per peer and insert events. **[Agent: go-fullstack]**
- [x] Extend `/api/service` handler to include `events[]` for the last hour. **[Agent: go-fullstack]**
- [x] `web/templates/cards/events.html`. **[Agent: go-fullstack]**

---

## Slice 11: 10-second auto-refresh + stale-data indicator (htmx)

**Outcome:** All cards refresh every 10s via htmx; "Stale data" pill appears when the API errors.

- [x] Vendor `web/static/htmx.min.js` (pinned htmx version, integrity-checked). **[Agent: go-fullstack]**
- [x] Add `hx-get="/api/snapshot"` `hx-trigger="every 10s"` `hx-target="this"` to each card; the API returns HTML fragments. **[Agent: go-fullstack]** _(implemented as a single `/partial/dashboard` swap target on `<main>` rather than per-card `hx-get` — see Slice 11 architecture decision: charts stay outside the swap so `<canvas>` elements aren't destroyed)_
- [x] Add stale-data pill rendered when handler returns degraded state. **[Agent: go-fullstack]**

---

## Slice 12: Mobile-responsive pass

**Outcome:** No horizontal scroll at 360 px; charts re-flow to single column < 600 px; touch targets ≥ 44 px.

- [x] Tweaks to `web/static/app.css` across cards + charts. **[Agent: go-fullstack]**
- [x] Verify in browser devtools at 360 px and 768 px viewports. **[Agent: go-fullstack]**

---

## Slice 12.5: Cap "Recent handshakes" to 10 most recent

**Outcome:** The handshake-events card shows only the 10 newest events (instead of all events in the last hour). Older entries roll off as new ones land.

- [x] Extend `db.QueryHandshakeEvents` with a `limit` parameter and switch the SQL to `ORDER BY ts DESC LIMIT ?` so it returns newest-first; update the test signature. Drop the `reverse` template helper since the API now returns DESC. **[Agent: go-fullstack]**
- [x] Update call sites (`handleGetService`, `handleIndex` via `buildPageData`) to pass `limit = 10`. Update the `events.html` template to drop the `reverse` filter. **[Agent: go-fullstack]**

---

## Slice 13: CI build pipeline (GH Actions OIDC → S3 upload)

**Outcome:** `git push origin main` (touching `dashboard/**`) builds the binary and uploads it to S3 automatically.

- [x] TF: GitHub OIDC IAM role scoped to this repo's `main` ref, with `s3:PutObject` on the artifact bucket. **[Agent: terraform-aws]**
- [x] `.github/workflows/dashboard-build.yml` — paths-filter `dashboard/**`, OIDC, `go test` then `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "-s -w"`, `aws s3 cp` to `main-${{ github.sha }}/wireguard-dashboard` and `latest/wireguard-dashboard`. Pin actions by SHA. **[Agent: cicd-github-actions]**
- [x] Push a no-op commit; verify the binary lands in S3 with both keys via `aws s3 ls`. **[Agent: cicd-github-actions]**

---

## Slice 14: CI deploy pipeline (GH Actions → SSM SendCommand)

**Outcome:** Push to main both builds AND deploys; running binary reflects the new SHA.

- [x] TF: extend the OIDC role with `ssm:SendCommand` on the EC2 ARN, scoped to a hard-pinned SSM document. **[Agent: terraform-aws]** _(implemented as a dedicated `dashboard-ci-deploy` role in the github-oidc roles map, not as an extension of the existing `dashboard-ci-build` role — least-privilege per workflow)_
- [x] TF: SSM document running `aws s3 cp` of the new binary + atomic `mv` + `systemctl restart wireguard-dashboard`. **[Agent: terraform-aws]**
- [x] `.github/workflows/dashboard-deploy.yml` — `workflow_run` after successful build (or `workflow_dispatch`). **[Agent: cicd-github-actions]**
- [x] Push a commit; verify the SSM run succeeds; on a VPN client, `curl http://172.16.15.1:8080/api/health` returns `{"ok":true}`; SSM session into the EC2 confirms `/proc/$(pidof wireguard-dashboard)/exe` resolves to the new binary path. **[Agent: cicd-github-actions]** _(End-to-end deploy succeeded 2026-05-11 after a 4-step IAM/script hardening pass: deploy role gained `ec2:DescribeInstances`, instance role gained `AmazonSSMManagedInstanceCore`, on-host `snap.amazon-ssm-agent.amazon-ssm-agent` was restarted to pick up the new permissions, and the SSM document's `set -euo pipefail` was dropped to `set -eu` because `aws:runShellScript` runs under `/bin/sh` (dash), not bash.)_

---

## Notes on agents

The `nextjs-fullstack` agent in `.claude/agents/` no longer fits. The `[Agent: go-fullstack]` label above is aspirational — until a `go-fullstack` agent file exists and Claude Code is restarted, runs fall back to `general-purpose` with a Go persona embedded inline. Cleanup recommended:

- Delete `.claude/agents/nextjs-fullstack.md`.
- Delete `.claude/skills/react-best-practices/` and `.claude/skills/typescript-development/` (no longer used).
- Create `.claude/agents/go-fullstack.md` describing the Go + htmx + Chart.js + SQLite mandate.
- Keep `cicd-github-actions`, `terraform-aws`, `linux-cloud-init`, `wireguard-networking`, `devsecops-quality` as-is.
