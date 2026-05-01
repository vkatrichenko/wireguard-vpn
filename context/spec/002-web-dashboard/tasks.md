# Tasks: Web Dashboard for WireGuard VPN

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Technical Specification:** [`technical-considerations.md`](./technical-considerations.md)

Vertical slices — each leaves the system in a runnable state and produces verifiable new value.

---

## Slice 1: Hello-world Next.js container reachable on EC2:8080 via SSH port-forward

**Outcome:** A running Next.js container on the WireGuard EC2 returning a placeholder page on port 8080. No public exposure yet — verified through an SSH tunnel over the already-open port 22.

- [x] Scaffold `dashboard/` — Next.js (App Router) with `app/page.tsx` ("WireGuard Dashboard") and `app/api/health/route.ts` returning `{ ok: true }`. **[Agent: nextjs-fullstack]**
- [x] Author `dashboard/Dockerfile` — multi-stage build, `linux/amd64`, exposes 8080. **[Agent: nextjs-fullstack]**
- [ ] TF: `terraform/modules/dashboard/ecr.tf` — private ECR repo + lifecycle policy (10 untagged + `main-*` for 30 days). **[Agent: terraform-aws]**
- [ ] TF: extend `terraform/modules/wireguard/iam.tf` with ECR pull permissions on the dashboard repo. **[Agent: terraform-aws]**
- [ ] Update `terraform/modules/wireguard/templates/user-data.txt` — install Docker, ECR-login, render `/etc/systemd/system/wireguard-dashboard.service`, `systemctl enable --now`. **[Agent: linux-cloud-init]**
- [ ] Build + push image manually from laptop: `docker buildx build --platform linux/amd64 -t <ECR>:bootstrap dashboard/ && docker push`. **[Agent: nextjs-fullstack]**
- [ ] `terraform apply` and `terraform apply -replace=module.wireguard.aws_instance.wireguard` to pick up new user-data. **[Agent: terraform-aws]**
- [ ] Verify: `ssh -L 8080:localhost:8080 ubuntu@<ec2>` then `curl http://localhost:8080/api/health` and open `http://localhost:8080/` in browser. **[Agent: nextjs-fullstack]**

---

## Slice 2: Public HTTPS at https://wg.vk.provectus.pro via Route53 + ACM + ALB

**Outcome:** The placeholder page is reachable in any browser at the canonical URL with a valid certificate.

- [ ] Manual (out-of-band): create NS records for `vk.provectus.pro` in the parent `provectus.pro` zone (different AWS account). **[Agent: terraform-aws]**
- [ ] TF: `terraform/modules/dashboard/dns.tf` — Route53 zone `vk.provectus.pro` + `wg` A-alias record pointing at the ALB. **[Agent: terraform-aws]**
- [ ] TF: `terraform/modules/dashboard/acm.tf` — ACM cert for `wg.vk.provectus.pro` with DNS validation in the new zone. **[Agent: terraform-aws]**
- [ ] TF: `terraform/modules/dashboard/sg.tf` — ALB SG (ingress 443+80 from `0.0.0.0/0`) + companion rule on EC2 SG (ingress 8080 from ALB SG). **[Agent: terraform-aws]**
- [ ] TF: `terraform/modules/dashboard/main.tf` — ALB across the existing 3 public subnets, target group with `/api/health` health check, listener 443 → TG, listener 80 → 301 redirect. **[Agent: terraform-aws]**
- [ ] `dig NS vk.provectus.pro` + wait for ACM to enter `ISSUED`; then `terraform apply`. **[Agent: terraform-aws]**
- [ ] Verify: open `https://wg.vk.provectus.pro` in browser → placeholder page renders, padlock valid, `http://...` redirects to HTTPS. **[Agent: terraform-aws]**

---

## Slice 3: HTTP Basic auth gate

**Outcome:** Visiting the URL prompts for credentials; correct creds → page; wrong → 401. ALB health check still green (`/api/health` exempt).

- [ ] Manual: create SSM SecureString params `/config/wireguard/dashboard/username` and `/config/wireguard/dashboard/password-hash` (≥ 20-char password, bcrypt-hashed). **[Agent: terraform-aws]**
- [ ] TF: extend EC2 IAM with `ssm:GetParameter` on those two paths. **[Agent: terraform-aws]**
- [ ] Update systemd unit to add `ExecStartPre` that fetches creds via `aws ssm get-parameter` and writes them into `EnvironmentFile`. **[Agent: linux-cloud-init]**
- [ ] Implement `dashboard/middleware.ts` — Basic-auth gate, exempt `/api/health`, verify bcrypt against env. **[Agent: nextjs-fullstack]**
- [ ] Vitest unit test for middleware (correct, wrong, missing header). **[Agent: nextjs-fullstack]**
- [ ] Build + push image; SSM-exec `docker pull && systemctl restart wireguard-dashboard`. **[Agent: nextjs-fullstack]**
- [ ] Verify: browser hits URL → 401 prompt; correct creds → page; ALB target health stays healthy. **[Agent: nextjs-fullstack]**

---

## Slice 4: WAF v2 rate-based rule on ALB

**Outcome:** A burst of >100 req/5 min from one IP gets 403 from WAF.

- [ ] TF: `terraform/modules/dashboard/waf.tf` — WAFv2 Web ACL (REGIONAL) with one rate-based rule (100 req/5 min/IP, BLOCK), associate with ALB, CloudWatch metrics enabled. **[Agent: terraform-aws]**
- [ ] Apply and verify: `for i in $(seq 1 200); do curl -sk https://wg.vk.provectus.pro/api/health -o /dev/null -w "%{http_code}\n"; done | sort | uniq -c` shows 403s appearing. **[Agent: terraform-aws]**

---

## Slice 5: `clients_config` schema migration to `{ name, address, public_key }`

**Outcome:** Same VPN connectivity as before, but Terraform now passes named clients and renders `/etc/wireguard-dashboard/clients.json` for the dashboard to consume.

- [ ] Update `terraform/modules/wireguard/variables.tf` — `clients_config` to `list(object({ name, address, public_key }))`. **[Agent: terraform-aws]**
- [ ] Update `terraform/modules/wireguard/templates/user-data.txt` peers section to consume the new shape; add a step that writes `/etc/wireguard-dashboard/clients.json` from the templated value. **[Agent: linux-cloud-init]**
- [ ] Update `terraform/dev/main.tf` to pass `[{ name = "vkatrychenko", address = "172.16.15.5/32", public_key = "..." }]`. **[Agent: terraform-aws]**
- [ ] Run `terraform plan -out=tfplan` and inspect: only EC2 user-data + clients.json should change. **[Agent: terraform-aws]**
- [ ] Apply + replace EC2; verify `/etc/wireguard-dashboard/clients.json` content via SSM session; existing VPN client still connects. **[Agent: linux-cloud-init]**

---

## Slice 6: Server endpoint info card

**Outcome:** Logged-in user sees public IP, port 51820, and server public key (with copy-to-clipboard).

- [ ] `lib/server-info.ts` — IMDSv2 for public IP; `wg show wg0 public-key` for server pubkey. **[Agent: nextjs-fullstack]**
- [ ] `app/api/server/route.ts`. **[Agent: nextjs-fullstack]**
- [ ] `components/ServerInfoCard.tsx` with copy-to-clipboard. **[Agent: nextjs-fullstack]**
- [ ] Wire into `app/page.tsx`. **[Agent: nextjs-fullstack]**
- [ ] Vitest test (mocked exec). **[Agent: nextjs-fullstack]**
- [ ] Build + deploy + verify in browser. **[Agent: nextjs-fullstack]**

---

## Slice 7: WireGuard service status + uptime card

**Outcome:** Card shows Running/Stopped + service uptime; goes red within ~10s when service stopped.

- [ ] `lib/systemd.ts` — `systemctl is-active wg-quick@wg0`, `systemctl show -p ActiveEnterTimestamp wg-quick@wg0`. **[Agent: nextjs-fullstack]**
- [ ] `app/api/service/route.ts`. **[Agent: nextjs-fullstack]**
- [ ] `components/ServiceStatusCard.tsx` + `UptimeCard.tsx`. **[Agent: nextjs-fullstack]**
- [ ] Vitest tests (running, stopped, never-started). **[Agent: nextjs-fullstack]**
- [ ] Update systemd unit to bind-mount `/run/dbus/system_bus_socket` and add `--pid host`. **[Agent: linux-cloud-init]**
- [ ] Verify: SSM-exec `systemctl stop wg-quick@wg0` → card shows "Service down". **[Agent: nextjs-fullstack]**

---

## Slice 8: Client list with online/offline + last handshake

**Outcome:** Table of configured peers with name, IP, status pill, last-handshake "X min ago".

- [ ] `lib/clients-config.ts` — read `/etc/wireguard-dashboard/clients.json` (bind-mount in systemd unit). **[Agent: nextjs-fullstack]**
- [ ] `lib/wg.ts` — `wg show wg0 dump` parser; `--cap-add NET_ADMIN` + `--network host` documented in unit file. **[Agent: nextjs-fullstack]**
- [ ] `app/api/clients/route.ts` — join wg-show output with clients.json. **[Agent: nextjs-fullstack]**
- [ ] `components/ClientList.tsx` (empty state included). **[Agent: nextjs-fullstack]**
- [ ] Vitest for `lib/wg.ts` (no peers / never-handshaked / normal). **[Agent: nextjs-fullstack]**
- [ ] RTL for `ClientList` (empty / populated / online vs offline pill). **[Agent: nextjs-fullstack]**
- [ ] Verify: configured peer visible; connect WG client → row flips to Online within ~3 min handshake window. **[Agent: nextjs-fullstack]**

---

## Slice 9: Per-client cumulative traffic in client list

**Outcome:** Each row shows bytes sent / received, updates after WG transfer.

- [ ] Extend `lib/wg.ts` to expose rx/tx fields. **[Agent: nextjs-fullstack]**
- [ ] Extend `ClientList` to show humanized bytes columns. **[Agent: nextjs-fullstack]**
- [ ] RTL for the new column. **[Agent: nextjs-fullstack]**
- [ ] Verify: traffic increases when WG client transfers. **[Agent: nextjs-fullstack]**

---

## Slice 10: Live system snapshot — CPU + memory + network rate

**Outcome:** Three live cards with current values (no charts yet).

- [ ] `lib/proc.ts` — read `/host/proc/stat`, `/host/proc/meminfo`, `/host/sys/class/net/wg0/statistics/{rx,tx}_bytes`, `/host/proc/uptime`. Compute CPU% + rates from prior in-memory sample. **[Agent: nextjs-fullstack]**
- [ ] Update systemd unit to bind-mount `/proc:/host/proc:ro` and `/sys:/host/sys:ro`. **[Agent: linux-cloud-init]**
- [ ] `app/api/snapshot/route.ts` — aggregate `{ system, traffic, clients, service, server }`. **[Agent: nextjs-fullstack]**
- [ ] `components/SystemCard.tsx` + `NetworkRateCard.tsx`. **[Agent: nextjs-fullstack]**
- [ ] Vitest for `lib/proc.ts` parsing. **[Agent: nextjs-fullstack]**
- [ ] Verify in browser: sane CPU% / memory% / rx-tx rate. **[Agent: nextjs-fullstack]**

---

## Slice 11: 24-hour history (SQLite poller + Recharts trend charts)

**Outcome:** Sparkline charts populate over time; persist across container restart.

- [ ] Add `wireguard-dashboard-data` Docker volume in systemd unit. **[Agent: linux-cloud-init]**
- [ ] `lib/db.ts` (better-sqlite3) — bootstrap `system_metrics`, `traffic_metrics`, `client_traffic` tables. **[Agent: nextjs-fullstack]**
- [ ] `instrumentation.ts` — 30s sampler + 10-min retention sweep (>25h). **[Agent: nextjs-fullstack]**
- [ ] `app/api/metrics/route.ts?range=24h`. **[Agent: nextjs-fullstack]**
- [ ] `components/charts/Trend.tsx` (Recharts `<AreaChart>`) for CPU, memory, rx, tx. **[Agent: nextjs-fullstack]**
- [ ] Vitest for retention sweeper boundary. **[Agent: nextjs-fullstack]**
- [ ] Verify: charts populate over a few minutes; restart container → history persists. **[Agent: nextjs-fullstack]**

---

## Slice 12: Recent handshake events (last hour)

**Outcome:** Card lists handshake events with timestamp + client name; updates within ~30s.

- [ ] Add `handshake_events` table to SQLite. **[Agent: nextjs-fullstack]**
- [ ] Extend poller to detect handshake-time changes per peer and insert events. **[Agent: nextjs-fullstack]**
- [ ] Extend `/api/service` route to include `events[]` for the last hour. **[Agent: nextjs-fullstack]**
- [ ] `components/EventsCard.tsx`. **[Agent: nextjs-fullstack]**
- [ ] Verify: handshake from a client appears within ~30s. **[Agent: nextjs-fullstack]**

---

## Slice 13: 10-second auto-refresh + stale-data indicator

**Outcome:** All cards refresh every 10s; "Stale data" pill appears when fetch fails.

- [ ] `usePoll` client hook — poll `/api/snapshot` every 10s; expose `data` + `isStale`. **[Agent: nextjs-fullstack]**
- [ ] Wire across page; show "Stale data" pill on failure. **[Agent: nextjs-fullstack]**
- [ ] RTL test for the hook (mocked fetch, success / failure cases). **[Agent: nextjs-fullstack]**
- [ ] Verify: cards update every 10s; toggle network off → stale pill appears, then disappears on recovery. **[Agent: nextjs-fullstack]**

---

## Slice 14: Mobile-responsive pass

**Outcome:** No horizontal scroll at 360 px; charts re-flow to single column < 600 px; touch targets ≥ 44 px.

- [ ] CSS/Tailwind tweaks across cards + charts. **[Agent: nextjs-fullstack]**
- [ ] Verify in browser devtools at 360 px and 768 px viewports. **[Agent: nextjs-fullstack]**

---

## Slice 15: CI build pipeline (GH Actions OIDC → ECR push)

**Outcome:** `git push origin main` (touching `dashboard/**`) builds and pushes a tagged image automatically.

- [ ] TF: GitHub OIDC IAM role scoped to this repo's `main` ref, with ECR push on the dashboard repo. **[Agent: terraform-aws]**
- [ ] `.github/workflows/dashboard-build.yml` — paths-filter `dashboard/**`, OIDC, `docker buildx build --platform linux/amd64`, push `main-${{ github.sha }}` and `latest`. **[Agent: cicd-github-actions]**
- [ ] Push a no-op commit; verify the image lands in ECR with both tags via `aws ecr describe-images`. **[Agent: cicd-github-actions]**

---

## Slice 16: CI deploy pipeline (GH Actions → SSM SendCommand)

**Outcome:** Push to main both builds AND deploys; running container reflects the new SHA.

- [ ] TF: extend the OIDC role with `ssm:SendCommand` on the EC2 ARN, scoped to a hard-pinned SSM document. **[Agent: terraform-aws]**
- [ ] TF: SSM document running `docker pull <ECR>:main-<sha> && systemctl restart wireguard-dashboard`. **[Agent: terraform-aws]**
- [ ] `.github/workflows/dashboard-deploy.yml` — `workflow_run` after successful build (or `workflow_dispatch`). **[Agent: cicd-github-actions]**
- [ ] Push a commit; verify the SSM run succeeds, then `docker ps` on the EC2 shows the new SHA tag. **[Agent: cicd-github-actions]**
