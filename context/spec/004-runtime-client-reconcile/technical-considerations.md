# Technical Specification: Runtime Client Reconcile

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Replace cloud-init's one-shot peer rendering with a runtime reconcile loop owned by the dashboard binary.

- **Terraform** writes the declared `clients_config` list to a new AWS SSM `SecureString` (Advanced tier) parameter and grants the EC2 instance role scoped `ssm:GetParameter` on it. The launch template's `user_data` is `ignore_changes`d on the instance (current behavior), so a `clients_config` change updates only the SSM parameter — never the running instance.
- **cloud-init** shrinks: `/etc/wireguard/wg0.conf` is rendered with the `[Interface]` section only (server private key, listen port, server CIDR, PostUp/PostDown NAT rules). The peers section is empty. The `/etc/wireguard-dashboard/clients.json` heredoc step is removed entirely. A new `/etc/wireguard-dashboard/config.yaml` is dropped with the SSM parameter path, region, and other tunables baked in.
- **The dashboard binary** gains a `ClientSource` abstraction (with `ssmSource` as the only v1 implementation, designed so a future `fileSource` slots in cleanly), a `Reconciler` that orchestrates fetch → validate → diff → `wg syncconf`, a Refresh & Apply UI with preview-diff, a `.conf` template downloader, and a reconcile audit log. On startup it runs one reconcile pass before serving HTTP, so first boot and instance replacement bring up peers automatically.
- **Peers live in `/etc/wireguard-dashboard/wg0.peers.conf`** (stripped peers-only WG format), owned by the dashboard. The server private key file (`/etc/wireguard/wg0.conf`) is never touched by the dashboard process. `wg syncconf wg0 <peers-file>` is the only kernel mutation; it is peer-only by design and leaves `[Interface]` and unrelated peer sessions untouched.
- **Migration is in-place.** `terraform apply` creates the SSM parameter and IAM grant; the existing CI pipeline (`dashboard-build.yml` → `dashboard-deploy.yml`) deploys the new binary; the dashboard auto-reconciles on restart. Steady-state peers are unchanged because SSM matches what's already loaded; net VPN downtime is zero.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Terraform module changes

All changes live in `terraform/modules/wireguard/`. The composing module (`terraform/dev/main.tf`) is unchanged except for picking up new outputs if the dashboard's `config.yaml` needs them rendered there.

| File | Change |
|------|--------|
| `main.tf` | New `aws_ssm_parameter "clients"` — `type = "SecureString"`, `tier = "Advanced"`, `value = jsonencode(var.clients_config)`. No `ignore_changes` — Terraform owns this resource end-to-end. |
| `variables.tf` | New `clients_ssm_param_name` variable (default `/config/${var.project_name}-${var.env}/clients`). |
| `iam.tf` | Extend `data.aws_iam_policy_document.wireguard_policy_doc` with a statement granting `ssm:GetParameter` and `ssm:GetParameters` on the new parameter ARN only (not the path wildcard). |
| `locals.tf` | Drop `local.clients_json` and `local.wg_client_data_json`. Remove `clients_json` and `peers` from the `templatefile(...)` map. Add `clients_ssm_param_name`, `aws_region`, `server_public_ip` to that map (consumed by user-data to render `config.yaml`). |
| `templates/user-data.txt` | Remove `${peers}` from the `wg0.conf` heredoc (the peers section becomes empty). Remove the `/etc/wireguard-dashboard/clients.json` heredoc step entirely. Add new heredoc that writes `/etc/wireguard-dashboard/config.yaml` with substituted SSM path + region. Append two new sudoers NOPASSWD lines for `wg syncconf` (see §2.6). |
| `outputs.tf` | New outputs `clients_ssm_param_name`, `clients_ssm_param_arn` for caller visibility. |

The `aws_instance.wireguard` resource's `lifecycle { ignore_changes = [user_data, user_data_base64] }` block stays as-is. A `clients_config` change updates only the SSM parameter, never the launch template version that the running instance references — satisfying functional-spec §2.1 AC#3 ("no effect on the running VPN") by construction.

### 2.2 Dashboard binary changes

#### New packages

| Package | Responsibility |
|---------|----------------|
| `internal/clientsource` | `ClientSource` Go interface (`Fetch(ctx) ([]Client, error)`) and the `ssmSource` implementation using `github.com/aws/aws-sdk-go-v2/service/ssm`. Region and parameter path come from the configfile. Returns `[]clientsfile.Client` (existing struct, reused for shape compatibility with the rest of the dashboard). |
| `internal/wgconf` | Pure helpers — `RenderPeers([]Client) []byte` emits stripped-form peers-only WG conf text suitable for `wg syncconf`. No I/O, fully unit-testable. |
| `internal/reconcile` | The reconciler. Orchestrates the full reconcile algorithm (see §2.5). Exposes `Apply(ctx, dryRun bool) (Result, error)` and `Result` carrying the diff, the interface-drift verdict, the applied flag, and any error. Holds the process-wide mutex that serializes concurrent calls. |
| `internal/configfile` | Reads `/etc/wireguard-dashboard/config.yaml` once at startup; struct values are consumed by the wiring in `cmd/wireguard-dashboard/main.go`. YAML via `gopkg.in/yaml.v3`. |

#### Modifications to existing packages

| File | Change |
|------|--------|
| `internal/clientsfile/clientsfile.go` | Repurpose as the **cache** reader. Default path changes from `/etc/wireguard-dashboard/clients.json` to `/etc/wireguard-dashboard/clients.yaml`. Struct + `readFunc` seam unchanged. Used by snapshot handlers when SSM is unreachable on a later Refresh. |
| `internal/db/db.go` + `db_test.go` | New `reconcile_events` table (see §2.3). New `InsertReconcileEvent(Result)`, `QueryReconcileEvents(limit int)` methods following existing patterns. |
| `internal/server/server.go` | Inject `clientsource.ClientSource` and `*reconcile.Reconciler` into the handler struct alongside existing dependencies. |
| `internal/server/handlers_reconcile.go` *(new)* | The HTTP surface — see §2.4. |
| `internal/server/handlers_clients.go` | Add `GET /api/clients/{name}/config-template` returning a `.conf` skeleton with `Content-Disposition: attachment; filename="<name>.conf"`. |
| `internal/server/handlers_snapshot.go` | When the snapshot handler renders the client list, the source of declared peers becomes the in-memory result of the most recent reconcile (fallback: cache file). Online/offline indicators still come from `wg show wg0 dump` via the existing `internal/wg` package. |
| `web/templates/cards/clients.html` | Add "Refresh" button (htmx `hx-post="/api/reconcile?dry_run=true"` targeting a new preview pane); add "Download config template" link per peer row. |
| `web/templates/cards/reconcile-preview.html` *(new)* | Diff table grouped by added / removed / updated / unchanged; warning banner for interface drift; Apply button (htmx `hx-post="/api/reconcile"`) gated on absence of drift. |
| `web/templates/cards/reconcile-history.html` *(new)* | Last 20 audit rows — timestamp + summary (`+3, −1, ~0`) + success/failure pill. |
| `cmd/wireguard-dashboard/main.go` | Add startup reconcile: after the configfile + serverinfo wiring, before `server.Listen`, call `reconciler.Apply(ctx, false)`. Log success or failure; do **not** fail process start on reconcile error — the operator should still be able to reach the UI to investigate. |

### 2.3 Data model changes

New SQLite table in `metrics.db` (the existing dashboard DB at `/var/lib/wireguard-dashboard/metrics.db`):

| Table | Column | Type | Notes |
|-------|--------|------|-------|
| `reconcile_events` | `id` | `INTEGER PRIMARY KEY AUTOINCREMENT` | |
| | `ts` | `INTEGER NOT NULL` | Unix epoch seconds (matches `handshake_events` shape) |
| | `added` | `INTEGER NOT NULL` | Count of peers added by this reconcile |
| | `removed` | `INTEGER NOT NULL` | Count of peers removed |
| | `updated` | `INTEGER NOT NULL` | Count of peers whose `address` or `public_key` changed |
| | `success` | `INTEGER NOT NULL` | 0 / 1, matches existing boolean convention |
| | `error` | `TEXT` | NULL on success; captured kernel/SDK error message on failure |

Index: `CREATE INDEX reconcile_events_ts_idx ON reconcile_events(ts DESC)`.

Retention: extended to **30 days** (vs the existing 25-hour sweep on metrics tables) — admin actions are sparse and the storage cost is negligible, and a 30-day window aligns with typical incident-investigation timelines. The existing `internal/poller` retention sweep gets a new pass for this table with a different cutoff.

### 2.4 API contracts

All under the existing `internal/server` package; htmx fragment responses where indicated.

| Method | Path | Purpose | Body / Query | Response |
|--------|------|---------|--------------|----------|
| `POST` | `/api/reconcile?dry_run=true` | Fetch source, validate, compute diff against kernel, render preview. **No kernel changes.** | none | `text/html` fragment (`reconcile-preview.html`) |
| `POST` | `/api/reconcile` | Re-fetch, validate, apply via `wg syncconf`, write cache + audit row, render result fragment | none | `text/html` fragment showing applied diff + success/failure pill |
| `GET`  | `/partial/reconcile-history` | Last N reconcile audit rows | `?limit=N` (default 20) | `text/html` fragment (`reconcile-history.html`) |
| `GET`  | `/api/clients/{name}/config-template` | Download `.conf` template for the named peer | path param | `text/plain` attachment, `Content-Disposition: attachment; filename="{name}.conf"` |

API notes:

- **Refresh and Apply are stateless.** Each `POST /api/reconcile` re-fetches from SSM. There is no server-side staging of "the diff you previewed." If the operator clicks Refresh, then someone runs another `terraform apply`, then they click Apply — the freshly fetched diff is what's applied. The preview is purely advisory.
- The Apply button in the preview fragment is rendered with `hx-post="/api/reconcile"` (no `dry_run`) and is **omitted from the fragment** when the preview detected interface drift. This is how the spec's §2.5 "Apply disabled on drift" is enforced.
- `404` on `config-template` when the named peer is absent from the cache; `502` (with the AWS error message) when SSM is unreachable on a Refresh.

#### `.conf` template shape

```
[Interface]
PrivateKey = <PASTE_PRIVATE_KEY_HERE>
Address    = <peer-address>
DNS        = 1.1.1.1, 9.9.9.9

[Peer]
PublicKey  = <server-public-key>
Endpoint   = <server-public-ip>:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
```

Substitutions come from configfile (`server_public_ip`, listen port) and the peer's declared `address`. The server public key is read via `sudo wg show wg0 public-key` (already wired in `internal/serverinfo`).

### 2.5 Reconcile algorithm

Pseudocode for `Reconciler.Apply(ctx, dryRun)`:

```
1.  ACQUIRE process-wide sync.Mutex (returns "reconcile in progress" if held)
2.  clients := source.Fetch(ctx)                     // SSM GetParameter
3.  validate(clients):
      - names unique
      - addresses unique
      - each address matches ^\d+\.\d+\.\d+\.\d+/32$ AND falls within configured wg.cidr
      - each public_key decodes as 32 raw bytes from base64
    On error: build Result{Err: ...}, audit (success=false), return
4.  kernelPeers := wgService.Show(ctx)               // sudo wg show wg0 dump (existing internal/wg)
5.  diff := computeDiff(clients, kernelPeers)
      - added:    in clients, not in kernel (by pubkey)
      - removed:  in kernel, not in clients
      - updated:  in both; address differs
      - unchanged: rest
6.  interfaceDrift := detectInterfaceDrift()         // see §2.7
7.  IF dryRun:
      return Result{Diff: diff, InterfaceDrift: interfaceDrift, Applied: false}
8.  IF interfaceDrift:
      audit (success=false, error="interface drift blocks apply")
      return Result{Diff: diff, InterfaceDrift: true, Applied: false}
9.  peersBytes := wgconf.RenderPeers(clients)
10. write peersBytes → /etc/wireguard-dashboard/wg0.peers.conf.new
      (mode 0640, owner root:wireguard-dashboard via the install-time directory perms)
11. exec "sudo wg syncconf wg0 /etc/wireguard-dashboard/wg0.peers.conf.new"
    IF non-zero exit:
      audit (success=false, error=<stderr>)
      delete the staging file
      return Result{Diff: diff, Applied: false, Err: ...}
12. os.Rename(.peers.conf.new, .peers.conf)          // atomic on the same filesystem
13. write clients → /etc/wireguard-dashboard/clients.yaml (cache for SSM-outage view)
14. db.InsertReconcileEvent(diff, success=true)
15. return Result{Diff: diff, Applied: true}
```

Two important properties:

- **Rename happens only after `wg syncconf` succeeds.** A kernel-side failure leaves the prior `.peers.conf` on disk so a reboot still loads the correct peers via the startup reconcile.
- **The mutex covers fetch through audit.** A second `Apply` call while one is in flight returns "in progress" rather than queueing — preventing torn writes and avoiding surprising operator UX.

### 2.6 Sudoers extension

`/etc/sudoers.d/wireguard-dashboard` gains two new NOPASSWD lines, written by cloud-init (see `terraform/modules/wireguard/templates/user-data.txt`):

```
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg syncconf wg0 /etc/wireguard-dashboard/wg0.peers.conf
wireguard-dashboard ALL=(root) NOPASSWD: /usr/bin/wg syncconf wg0 /etc/wireguard-dashboard/wg0.peers.conf.new
```

Plus the existing four (`wg show wg0 public-key`, `wg show wg0 dump`, `systemctl is-active wg-quick@wg0`, `systemctl show -p ActiveEnterTimestamp wg-quick@wg0`).

Pinning to two specific paths instead of wildcarding `wg syncconf wg0 *` keeps the least-privilege intent of the v3 spec intact.

### 2.7 First-boot ordering and interface-drift detection

#### Boot ordering

Today's systemd ordering (from `user-data.txt:120-140`) is unchanged:

- `wireguard-dashboard.service` has `After=network-online.target wg-quick@wg0.service` and `Requires=wg-quick@wg0.service`.

Flow on a fresh boot or instance replacement:

1. cloud-init renders `/etc/wireguard/wg0.conf` with `[Interface]` only.
2. `wg-quick@wg0.service` starts → wg0 interface comes up, kernel has zero peers.
3. `wireguard-dashboard.service` starts → main.go runs `reconciler.Apply(ctx, false)` before HTTP serve → peers materialize via `wg syncconf` → HTTP starts.

Brief "no peers" window between steps 2 and 3 — measured in single-digit seconds on a `t3a.micro`. Acceptable for first boot and instance replacement (both rare).

#### Interface-drift detection

For every Refresh, the reconciler compares:

- **On-disk `[Interface]`** (parsed from `/etc/wireguard/wg0.conf`): listen port, server private key → derive server public key.
- **Running kernel `[Interface]`** (from `sudo wg show wg0`): listen port + server public key directly.

If listen port or server public key differs, `interfaceDrift = true`, the preview surfaces a warning banner, and Apply is disabled. No path in this spec produces interface drift (peers-only reconcile + immutable cloud-init [Interface]) — the detector is a safety net for out-of-band edits.

### 2.8 New dashboard config file

`/etc/wireguard-dashboard/config.yaml`, rendered by cloud-init via templatefile substitution. Owner `root:wireguard-dashboard`, mode `0640`. Not operator-edited on a deployed box; values are pinned at apply time.

```yaml
client_source:
  provider: aws
  aws:
    ssm_parameter: ${clients_ssm_param_name}
    region: ${aws_region}

wg:
  cidr: ${wg_server_net}              # e.g. "172.16.15.1/24" — used for IP-membership validation in step 3 of the reconcile algorithm
  listen_port: ${wg_server_port}
  server_endpoint: ${server_public_ip}

paths:
  peers_file: /etc/wireguard-dashboard/wg0.peers.conf
  clients_cache: /etc/wireguard-dashboard/clients.yaml
  reconcile_db: /var/lib/wireguard-dashboard/metrics.db
```

---

## 3. Impact and Risk Analysis

### 3.1 System dependencies

| Dependency | Type | Impact |
|------------|------|--------|
| AWS SSM Parameter Store | New runtime read path | Already an apply-time dependency (server private key). Project has operational familiarity. Failure surfaces in UI; kernel state stays at last-known good. |
| `github.com/aws/aws-sdk-go-v2/service/ssm` | New Go dependency | Standard AWS SDK; pinned via `go.mod`. Replaces shell-out to AWS CLI for cleaner error handling. |
| `gopkg.in/yaml.v3` | New Go dependency | Config-file parsing. Stable, widely used. |
| `wg`, `wg-quick` binaries | Existing | No version change. `wg syncconf` requires wireguard-tools ≥ 1.0.20210914 (Ubuntu 24.04 has 1.0.20210914 in main). |
| Existing CI pipeline (`dashboard-build.yml`, `dashboard-deploy.yml`) | Unchanged | New binary ships via the same OIDC → S3 → SSM SendCommand path. No workflow edits required for v1. |
| Existing TF state | Additive | New `aws_ssm_parameter "clients"` is a new resource; existing instance is preserved by `ignore_changes`. |

### 3.2 Potential risks & mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| Operator forgets to click Apply after `terraform apply` | Medium | New peer can't connect until Apply is clicked | UI nudge: surface a "Pending change in SSM" badge when the fetched SSM payload's content hash differs from the cache's hash. Implementation deferred to a v1.1 polish task; spec out-of-scope. |
| SSM throttling on rapid reconciles | Low | Refresh returns a 5xx | Single-operator usage is well below SSM's 40 reqs/sec `GetParameter` quota. No special handling. |
| Validation lets through an entry that `wg syncconf` rejects | Low | Reconcile fails; audit row records error; kernel state unchanged | Validation catches the common cases (uniqueness, regex, CIDR membership, base64 → 32 bytes); kernel-side syncconf error is surfaced in the UI. |
| Operator manually edits `/etc/wireguard-dashboard/wg0.peers.conf` | Low | Next reconcile silently overwrites | Documented in the file's header comment that it is dashboard-owned. Audit log captures every change so unexpected churn is visible. |
| Reboot drops peers until startup reconcile completes | Low (reboot is rare) | < 5 s of no peers after wg-quick comes up | Startup reconcile is the first action in `main.go`. Single-digit-second window accepted. |
| SSM parameter exceeds 8 KB Advanced tier ceiling | Low until ~50 clients | `aws_ssm_parameter.PutParameter` fails at `terraform apply` | Hard ceiling acknowledged in the functional spec out-of-scope. Migration to a hierarchy (one param per client) deferred to a later spec. |
| Interface drift between on-disk `wg0.conf` and kernel | Very low (no in-design path produces it) | Apply disabled; operator investigates manually | Drift detection on every Refresh; warning banner; Apply gated. Detector is a safety net, not a feature. |
| New SSM `GetParameter` IAM grant accidentally too broad | Low | Privilege creep | IAM policy statement targets the exact parameter ARN — not a path wildcard. Reviewed in `terraform/modules/wireguard/iam.tf` diff. |
| Existing `clients.json` left on disk after migration | Low | Stale data confusion | Cloud-init's removal of the heredoc means new instances never write the legacy file. Existing instance: stale file is harmless (no code reads it post-deploy). Optional one-shot cleanup in the deploy SSM document. |

---

## 4. Testing Strategy

### 4.1 Unit tests (Go, table-driven, in-package `_test.go`)

| Package | Coverage |
|---------|----------|
| `internal/wgconf` | `RenderPeers` byte-exact output for: empty list, single peer, multiple peers, peer with multiple `AllowedIPs`. |
| `internal/reconcile` | Diff computation (added / removed / updated / unchanged) across table-driven inputs. Use fake `ClientSource` + the existing `runFunc` seam in `internal/wg` for fake kernel state. Verify: validation errors short-circuit before any I/O; rename only after a successful syncconf exec; mutex serializes concurrent calls (returns "in progress"); interface-drift short-circuits before any I/O. |
| `internal/clientsource` | Fake `ssm.Client` interface; test JSON decode for single client, multiple clients, empty list, malformed JSON, missing parameter, throttling error. |
| `internal/db` | `reconcile_events` insert + query (limit, DESC ordering) following the shape of the existing `db_test.go` cases. |
| `internal/configfile` | YAML parse: happy path; missing required fields; unknown fields tolerated. |

### 4.2 End-to-end integration test (manual, scripted in eventual `tasks.md`)

On the actual `wireguard-vpn-test` EC2:

1. **Baseline.** VPN connected from operator laptop; dashboard reachable at `http://172.16.15.1:8080`; `wg show wg0 dump` lists current peers; capture handshake counters.
2. **TF apply.** Add a new client (`testpeer`) to `clients_config` in `terraform/dev/main.tf`. `terraform plan` — verify the diff is `aws_ssm_parameter.clients` (and IAM policy on first run) only, no instance changes.
3. **Verify in-flight VPN unaffected.** `terraform apply tfplan`. Confirm `wg show wg0 transfer` for existing peers shows uninterrupted byte counter growth.
4. **Refresh in dashboard.** Click Refresh. Verify preview shows `+1 added (testpeer)`, others unchanged.
5. **Apply.** Click Apply. Verify reconcile completes < 1 s; reconcile-history card shows the new row; `wg show wg0 dump` now includes `testpeer`.
6. **Connect testpeer.** Generate a keypair for testpeer locally, download config template, fill in private key, connect from a second device. Confirm handshake within 30 s.
7. **Existing-peer integrity.** Re-check existing peers' handshake counters from step 1 — must not have reset, must show forward progress.
8. **Removal.** Remove `testpeer` from `clients_config`. `terraform apply`. Refresh → preview shows `-1 removed (testpeer) — active session, will be dropped`. Apply. Verify `testpeer` loses connectivity within seconds; vkatrichenko's session is unaffected.
9. **`.conf` template download.** From the dashboard, click "Download config template" for an existing peer. Inspect the file: `[Interface] Address = …`, `<PASTE_PRIVATE_KEY_HERE>` placeholder, `[Peer] PublicKey = …`, `Endpoint = <public-ip>:51820`. Filename `<name>.conf`.

### 4.3 Negative-path tests (manual)

| Scenario | Expected behavior |
|----------|-------------------|
| SSM `GetParameter` IAM permission temporarily revoked | Refresh shows the AWS error inline; Apply button hidden; cached client list still rendered in the snapshot card. |
| Malformed JSON written to the SSM parameter | Refresh validation error surfaced inline; no kernel state change; no audit row (validation happens pre-audit). |
| `wg syncconf` killed mid-flight (simulated by removing the sudoers grant temporarily) | Apply audit row recorded with `success=0` and the exec error; staging `.peers.conf.new` file cleaned up; running peers untouched. |
| Concurrent Apply clicks from two browser tabs | First request acquires the mutex and runs to completion; second returns "reconcile in progress, retry" without touching kernel. |
| Manual edit of `/etc/wireguard/wg0.conf` (change listen port out-of-band) | Refresh banner: "Interface drift detected — listen port mismatch." Apply button hidden until drift is resolved. |

### 4.4 Migration verification

Specifically test the in-place migration story:

1. Pre-migration baseline: existing VPN running, current dashboard binary, current Terraform module shape, `clients.json` present.
2. `terraform apply` — verify only `aws_ssm_parameter.clients` and `aws_iam_policy_document.wireguard_policy_doc` (renderered into the existing policy) change; instance is not replaced; running VPN unaffected.
3. Git push to `main` touching `dashboard/**` — verify `dashboard-build.yml` and `dashboard-deploy.yml` run to green; new binary is on the box.
4. Confirm: dashboard restart auto-reconciles; SSM payload matches the kernel state; reconcile-history shows the startup row with `+0, -0, ~0`; VPN clients (including the operator's own laptop running the test) experience zero handshake interruption.

A failure at any of these steps points to a specific migration risk that needs surfacing back to the spec before claiming done.
