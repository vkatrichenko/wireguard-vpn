# Tasks: Runtime Client Reconcile

- **Functional Specification:** [`functional-spec.md`](./functional-spec.md)
- **Technical Specification:** [`technical-considerations.md`](./technical-considerations.md)

> **Migration note (2026-05-15):** Slices 1–3 are additive scaffolding — they introduce the new SSM/dashboard pieces without changing how peers actually reach the kernel. **Slice 4 is the in-place cutover** — that's where peers stop coming from cloud-init and start coming from the dashboard's reconcile path. Verify Slice 4 carefully against a live VPN session before continuing. Slices 5–7 are independent polish on top of the cutover.

Vertical slices — each leaves the system runnable with new verifiable value.

---

## Slice 1: SSM parameter + IAM grant (TF-only, runtime unchanged)

**Outcome:** After `terraform apply`, an SSM `SecureString` parameter contains the current `clients_config` as JSON. The EC2 instance role grants `ssm:GetParameter` on that exact ARN. The running VPN is unchanged; the dashboard still reads from the old `/etc/wireguard-dashboard/clients.json`. This is purely additive infrastructure.

- [ ] TF: add `aws_ssm_parameter "clients"` to `terraform/modules/wireguard/main.tf` — `type = "SecureString"`, `tier = "Advanced"`, `value = jsonencode(var.clients_config)`, `name = var.clients_ssm_param_name`. No `lifecycle.ignore_changes`. **[Agent: terraform-aws]**
- [ ] TF: add `clients_ssm_param_name` variable to `terraform/modules/wireguard/variables.tf` (default `/config/${var.project_name}-${var.env}/clients`, description explaining runtime read path). **[Agent: terraform-aws]**
- [ ] TF: extend `data.aws_iam_policy_document.wireguard_policy_doc` in `terraform/modules/wireguard/iam.tf` with a new statement (`sid = "SSMGetClientsParameter"`) granting `ssm:GetParameter` and `ssm:GetParameters` on the exact parameter ARN (no wildcards). **[Agent: terraform-aws]**
- [ ] TF: add `clients_ssm_param_name` and `clients_ssm_param_arn` outputs to `terraform/modules/wireguard/outputs.tf`. **[Agent: terraform-aws]**
- [ ] Run `make pre-commit` from repo root — fmt, tflint, trivy, terraform-docs all green. **[Agent: devsecops-quality]**
- [ ] `cd terraform/dev && terraform plan -out=tfplan` — verify the diff is exactly: `+ aws_ssm_parameter.clients`, `~ aws_iam_policy_document.wireguard_policy_doc` (new statement), and no changes to `aws_instance.wireguard` or `aws_launch_template.wireguard`. **[Agent: terraform-aws]**
- [ ] `terraform apply tfplan`. Verify by `AWS_PROFILE=csm aws ssm get-parameter --name /config/wireguard-vpn-test/clients --with-decryption --query Parameter.Value --output text | jq .` — output matches the HCL `clients_config` list exactly (same names, addresses, public_keys). **[Agent: terraform-aws]**
- [ ] Confirm the running VPN is unaffected: from the operator laptop on VPN, `curl http://172.16.15.1:8080/api/health` returns `{"ok":true}`; existing peer's handshake counter (`sudo wg show wg0 dump` via dashboard or SSM session) shows uninterrupted forward progress across the apply. **[Agent: wireguard-networking]**

---

## Slice 2: Dashboard fetches from SSM at startup (log-only, no behavior change)

**Outcome:** The dashboard binary reads its new `config.yaml` at startup, instantiates the SSM client source, fetches the clients list once, and logs the count. No reconcile, no kernel writes, no UI changes. The old `clients.json` path remains the source of truth for the snapshot endpoint. Verifies the SSM read path end-to-end on the actual instance.

- [ ] Add `internal/configfile/configfile.go` — struct + `Load(path string)`. Fields: `ClientSource{Provider string; AWS struct{SsmParameter, Region string}}`, `WG{CIDR, ListenPort, ServerEndpoint}`, `Paths{PeersFile, ClientsCache, ReconcileDB}`. YAML via `gopkg.in/yaml.v3` (add to `go.mod`). **[Agent: go-fullstack]**
- [ ] Add `internal/clientsource/clientsource.go` — `ClientSource` interface (`Fetch(ctx) ([]clientsfile.Client, error)`, `Name() string`) and `ssmSource` implementation using `github.com/aws/aws-sdk-go-v2/service/ssm` (`GetParameter` with `WithDecryption=true`, parses `Parameter.Value` as JSON into `[]clientsfile.Client`). Add `github.com/aws/aws-sdk-go-v2/config` and `service/ssm` to `go.mod`. **[Agent: go-fullstack]**
- [ ] Unit tests in `internal/clientsource/clientsource_test.go` — fake `ssm.Client` interface (whatever the smallest interface the impl needs). Cases: single client, multiple clients, empty list, malformed JSON, missing-parameter error, throttling error. **[Agent: go-fullstack]**
- [ ] Wire startup in `cmd/wireguard-dashboard/main.go`: load `configfile`, construct `ssmSource`, call `source.Fetch(ctx)` once, log the count and pubkey list at INFO level. Do **not** fail process start on fetch error — log and continue. **[Agent: go-fullstack]**
- [ ] TF: extend `terraform/modules/wireguard/locals.tf` — add `clients_ssm_param_name`, `aws_region`, `server_public_ip` to the `templatefile(...)` map for user-data. **[Agent: terraform-aws]**
- [ ] cloud-init: in `terraform/modules/wireguard/templates/user-data.txt`, add a heredoc step that writes `/etc/wireguard-dashboard/config.yaml` with the substituted values (owner `root:wireguard-dashboard`, mode 0640). Place this step *before* the dashboard binary is started but *after* the system user is created. All bash `$` chars escaped as `$$`. **[Agent: linux-cloud-init]**
- [ ] Build + upload binary via existing CI: git push to `main` touching `dashboard/**`. Verify the GH Actions workflow runs to green, S3 has the new artifact, SSM SendCommand deploys + restarts the service. **[Agent: cicd-github-actions]**
- [ ] **Manual on-host step (one-time migration):** the existing instance's cloud-init does not re-run, so `/etc/wireguard-dashboard/config.yaml` won't appear automatically. Open an SSM Session to the EC2 and drop the file manually (operator can copy the rendered content from `terraform output` and use `sudo tee`). Future instance replacements pick it up from user-data. **[Agent: linux-cloud-init]**
- [ ] Verify on host via `sudo journalctl -u wireguard-dashboard -n 50` — the startup logs include something like `"ssm client source: fetched N peers"` and the count matches `clients_config` length. **[Agent: go-fullstack]**

---

## Slice 3: Refresh + preview UI (dry-run reconcile, zero kernel writes)

**Outcome:** A "Refresh" button appears on the clients card. Clicking it fetches from SSM, computes the diff against running kernel state (`wg show wg0 dump`), and renders the preview fragment. The diff should be empty in steady state (SSM matches kernel because both were sourced from the same `clients_config` at apply time). No Apply button yet. No kernel writes. This slice validates the diff math against a real VPN.

- [ ] Add `internal/wgconf/wgconf.go` — `RenderPeers([]Client) []byte` producing stripped-form peers-only WG conf text. Each peer = `[Peer]\nPublicKey = ...\nAllowedIPs = ...\n`. Pure, no I/O. **[Agent: wireguard-networking]**
- [ ] Unit tests `internal/wgconf/wgconf_test.go` — empty list, single peer, multiple peers; byte-exact output. **[Agent: wireguard-networking]**
- [ ] Add `internal/reconcile/reconcile.go` — `Reconciler` struct (holds `ClientSource`, `wg.Service`, `configfile.Config`, `*sql.DB`, `sync.Mutex`). `Apply(ctx, dryRun=true)` method: fetch → validate (name/address uniqueness, regex, CIDR membership, base64→32B) → `wg show wg0 dump` → compute `Diff{Added, Removed, Updated, Unchanged []Client}` → return `Result{Diff, Applied: false}`. **Do not** touch kernel or files in this slice. **[Agent: go-fullstack]**
- [ ] Unit tests `internal/reconcile/reconcile_test.go` for diff computation only — fake source, fake `wg.Service`. Cases: empty kernel/empty source, added-only, removed-only, updated address, updated pubkey, all unchanged, validation failures (duplicate name, bad CIDR, bad base64). **[Agent: go-fullstack]**
- [ ] Add `internal/server/handlers_reconcile.go` — `POST /api/reconcile` handler that requires `dry_run=true` query param (any other value returns 405 "not yet implemented" in this slice). Calls `reconciler.Apply(ctx, true)`, renders `web/templates/cards/reconcile-preview.html` with the result. **[Agent: go-fullstack]**
- [ ] Add `web/templates/cards/reconcile-preview.html` — diff table grouped Added / Removed / Updated / Unchanged; "active session" annotation for removed peers (cross-reference last-handshake from kernel state). **No Apply button** in this slice — placeholder text instead. **[Agent: go-fullstack]**
- [ ] Modify `web/templates/cards/client-list.html` (or wherever the clients card lives — likely `web/templates/cards/clients.html` per the 002 work) to add a "Refresh" button with `hx-post="/api/reconcile?dry_run=true"`, `hx-target="#reconcile-preview-pane"`, `hx-swap="innerHTML"`. Add the empty `<div id="reconcile-preview-pane">` adjacent. **[Agent: go-fullstack]**
- [ ] Build + deploy via CI. **[Agent: cicd-github-actions]**
- [ ] Verify: from the operator laptop on VPN, open `http://172.16.15.1:8080/`. Click "Refresh". The preview pane shows the diff — should be **"No changes"** because SSM and kernel agree. **[Agent: go-fullstack]**
- [ ] Negative-path verification: temporarily add a fake client to `terraform/dev/main.tf` `clients_config`, `terraform apply` (this only updates SSM), then click Refresh in the dashboard. Preview should show `+1 added (fake-peer)`. Roll back the HCL change with another apply; Refresh; preview returns to "No changes". The running VPN was never touched throughout. **[Agent: terraform-aws]**

---

## Slice 4: Reconcile Apply + cutover (kernel mutation; peers move from cloud-init to dashboard)

**Outcome:** Clicking Apply on the preview actually reconciles the kernel via `wg syncconf`. Adding/removing a client now goes: edit HCL → `terraform apply` → Refresh → Apply → live. cloud-init stops rendering peers into `wg0.conf` and stops writing `clients.json`. The dashboard owns peer state via `/etc/wireguard-dashboard/wg0.peers.conf`. Startup reconcile in `main.go` ensures peers come up automatically on reboot or instance replacement. **This is the migration slice — verify carefully against a live VPN.**

- [ ] Extend sudoers in `terraform/modules/wireguard/templates/user-data.txt` — add two new NOPASSWD lines pinned to the exact peers file paths (no wildcards), per tech-spec §2.6. **[Agent: linux-cloud-init]**
- [ ] Extend `internal/db/db.go` with the `reconcile_events` table (columns + index per tech-spec §2.3); `InsertReconcileEvent(diff Result)` and `QueryReconcileEvents(limit int)` methods. Migration runs on dashboard startup like the existing tables. **[Agent: go-fullstack]**
- [ ] Unit tests `internal/db/db_test.go` for new methods — insert + query (limit, DESC ordering). **[Agent: go-fullstack]**
- [ ] Extend `internal/reconcile.Apply(ctx, dryRun)` to handle `dryRun=false`: write peers file to `…/wg0.peers.conf.new` (mode 0640), exec `sudo wg syncconf wg0 …/wg0.peers.conf.new`, on success atomic-rename to `wg0.peers.conf`, write clients cache to `clients.yaml`, insert audit row. On failure: cleanup staging file, audit row with `success=0` and stderr. Mutex covers fetch through audit. **[Agent: go-fullstack]**
- [ ] Reconciler tests for the apply path — fake `wg.Service`, fake exec (extend the existing `runFunc` seam pattern), fake DB. Cases: successful apply, `wg syncconf` non-zero exit (no rename, audit failure row), mutex serialization ("in progress" on second call). **[Agent: go-fullstack]**
- [ ] Extend `handlers_reconcile.go`: `POST /api/reconcile` (no `dry_run` query param) calls `reconciler.Apply(ctx, false)` and renders the result fragment. Update `reconcile-preview.html` to include an `<button hx-post="/api/reconcile" hx-target="#reconcile-result-pane">Apply</button>` rendered only when `result.InterfaceDrift == false`. **[Agent: go-fullstack]**
- [ ] Wire startup reconcile in `cmd/wireguard-dashboard/main.go`: after `configfile.Load` and `clientsource` construction, before `server.Listen`, call `reconciler.Apply(ctx, false)`. Log success or failure. Do **not** fail process start on reconcile error. **[Agent: go-fullstack]**
- [ ] TF: remove `${peers}` substitution from the `wg0.conf` heredoc in `terraform/modules/wireguard/templates/user-data.txt`. Remove the `/etc/wireguard-dashboard/clients.json` heredoc step entirely. Drop `local.clients_json` and `local.wg_client_data_json` from `terraform/modules/wireguard/locals.tf`. Drop `clients_json` and `peers` from the `templatefile(...)` map. **[Agent: linux-cloud-init]**
- [ ] Run `make pre-commit` — all green. **[Agent: devsecops-quality]**

**Migration verification (do this in order — read every step before starting):**

- [ ] **Pre-flight on the current instance.** Open an SSM Session, manually drop the two new sudoers lines into `/etc/sudoers.d/wireguard-dashboard` (validate with `visudo -c -f` first; atomic mv per the existing pattern in user-data.txt). The existing instance was launched with the old sudoers content, and cloud-init does not re-run. **[Agent: linux-cloud-init]**
- [ ] **Capture baseline.** From the operator laptop on VPN: note the exact `Last Handshake` time and `transfer rx/tx` counters for `vkatrychenko` peer from `sudo wg show wg0 dump`. **[Agent: wireguard-networking]**
- [ ] **Deploy the new binary via CI.** Git push to `main`. Watch `dashboard-build.yml` and `dashboard-deploy.yml` run to green. **[Agent: cicd-github-actions]**
- [ ] **Watch the service restart.** From the SSM Session: `sudo journalctl -u wireguard-dashboard -f` during the deploy. After restart, expect a log line like `"startup reconcile: 1 unchanged, 0 added, 0 removed"` — the diff should be empty because SSM and the kernel already agree. **[Agent: go-fullstack]**
- [ ] **Confirm zero VPN downtime.** On the operator laptop: VPN client should never have disconnected. `sudo wg show wg0 transfer` shows the `vkatrychenko` peer's byte counter has continued growing past the baseline; `Last Handshake` time has not reset to "now". **[Agent: wireguard-networking]**
- [ ] **TF apply the user-data shrink.** Run `terraform plan -out=tfplan` from `terraform/dev/`. Expected diff: launch template version bumps (user-data changed) but `aws_instance.wireguard` is unchanged (because of `ignore_changes = [user_data, user_data_base64]`). Apply. Confirm again that the running VPN is unaffected. **[Agent: terraform-aws]**
- [ ] **End-to-end peer-add test.** Add a new entry to `clients_config` (`testpeer` with a freshly generated keypair). `terraform apply tfplan`. From the dashboard click Refresh — preview shows `+1 added (testpeer)`. Click Apply — reconcile completes < 1 s; audit row recorded. `sudo wg show wg0 dump` now includes testpeer. Connect from a second device — handshake succeeds within 30 s. **[Agent: wireguard-networking]**
- [ ] **End-to-end peer-remove test.** Remove `testpeer` from `clients_config`. `terraform apply`. Refresh — preview shows `-1 removed (testpeer) — active session, will be dropped`. Apply. The testpeer device loses connectivity within seconds. `vkatrychenko` peer's `Last Handshake` time has not reset. **[Agent: wireguard-networking]**
- [ ] **Reboot test (optional but recommended).** SSM Session: `sudo reboot`. Wait for instance to come back. From operator laptop, verify VPN reconnects within ~30 s; dashboard reachable at `http://172.16.15.1:8080/`; `journalctl -u wireguard-dashboard` shows the startup reconcile ran successfully. **[Agent: linux-cloud-init]**

---

## Slice 5: `.conf` template download

**Outcome:** Each row in the client list has a "Download config template" link. Clicking it downloads `<peer-name>.conf` with the server pubkey + endpoint + the peer's address filled in, and `<PASTE_PRIVATE_KEY_HERE>` as the private-key placeholder.

- [ ] Add `GET /api/clients/{name}/config-template` handler in `internal/server/handlers_clients.go` — looks up the peer in the cache (`clients.yaml`); if missing returns 404; otherwise returns `text/plain` with `Content-Disposition: attachment; filename="<name>.conf"` and the body per tech-spec §2.4 ".conf template shape". Server endpoint + public key come from `configfile` and `internal/serverinfo` respectively. **[Agent: go-fullstack]**
- [ ] Unit test the handler — happy path (response body matches expected template; headers correct), 404 on missing peer. **[Agent: go-fullstack]**
- [ ] Update `web/templates/cards/client-list.html` (or equivalent — locate by searching for the peer table) to add a "Download" link per row: `<a href="/api/clients/{{ .Name }}/config-template">Download config</a>`. **[Agent: go-fullstack]**
- [ ] Build + deploy via CI. **[Agent: cicd-github-actions]**
- [ ] Verify: from operator laptop on VPN, click "Download" on the `vkatrychenko` row. Inspect the downloaded file — `[Interface] Address = 172.16.15.6/32`, `PrivateKey = <PASTE_PRIVATE_KEY_HERE>`, `[Peer] PublicKey = <server-pubkey>`, `Endpoint = <ec2-public-ip>:51820`, `AllowedIPs = 0.0.0.0/0, ::/0`. Filename is `vkatrychenko.conf`. **[Agent: go-fullstack]**

---

## Slice 6: Reconcile-history card (last 20 audit rows)

**Outcome:** A new card on the dashboard shows the last 20 reconcile events with timestamp, diff summary (`+N, -M, ~K`), and success/failure pill. Validates the audit log accumulates correctly.

- [ ] Add `GET /partial/reconcile-history` handler in `internal/server/handlers_reconcile.go` — accepts `?limit=N` (default 20, cap 100). Calls `db.QueryReconcileEvents(limit)`, renders `web/templates/cards/reconcile-history.html`. **[Agent: go-fullstack]**
- [ ] Add `web/templates/cards/reconcile-history.html` — table with columns: timestamp ("3 min ago"), diff summary (`+3, -1, ~0`), success pill, optional error message tooltip on failed rows. Empty state: "No reconciles recorded yet." **[Agent: go-fullstack]**
- [ ] Wire the card into the main dashboard layout (`web/templates/dashboard.html` or whatever the current root template is) with htmx auto-refresh every 30 s (`hx-trigger="every 30s"`). **[Agent: go-fullstack]**
- [ ] Extend `internal/poller`'s retention sweep to handle `reconcile_events` with a **30-day** cutoff (the metrics tables stay at 25 hours). **[Agent: go-fullstack]**
- [ ] Add retention-sweep test in `internal/poller` (or wherever the existing retention test lives) — verify rows older than 30 days are deleted; rows within 30 days are kept. **[Agent: go-fullstack]**
- [ ] Build + deploy via CI. **[Agent: cicd-github-actions]**
- [ ] Verify: trigger a few reconciles (Refresh → Apply on a no-op diff is fine — still records an audit row with `+0, -0, ~0`). Confirm the history card lists each one. After ~10 minutes, auto-refresh shows the same rows without manual page reload. **[Agent: go-fullstack]**

---

## Slice 7: Interface-drift detection in the preview

**Outcome:** If on-disk `/etc/wireguard/wg0.conf` `[Interface]` disagrees with running kernel `[Interface]` (listen port or server public key), the preview surfaces a warning banner and the Apply button is omitted. Pure detection — no apply path for interface changes (out-of-scope per spec §2.5).

- [ ] Add a small parser in `internal/wgconf/parser.go` that extracts `[Interface] { PrivateKey, ListenPort, Address }` from a wg-quick conf file. Derive server public key from the private key using `golang.org/x/crypto/curve25519` (or shell out to `wg pubkey` if cleaner — note the latter requires a sudoers grant). **[Agent: wireguard-networking]**
- [ ] Add `detectInterfaceDrift()` helper in `internal/reconcile/reconcile.go` — reads `wg0.conf` `[Interface]` and compares against `wg show wg0` listen port + server public key from `internal/serverinfo`. Returns a `DriftReport{HasDrift bool; Fields []string}`. **[Agent: wireguard-networking]**
- [ ] Plumb `DriftReport` into `Result` and through the preview template. Update `reconcile-preview.html`: when `result.InterfaceDrift.HasDrift`, render a warning banner listing the drifted fields, and **omit the Apply button**. **[Agent: go-fullstack]**
- [ ] Unit test in `internal/reconcile/reconcile_test.go` — table-driven: no drift, listen-port drift, server-key drift, both. **[Agent: go-fullstack]**
- [ ] Build + deploy via CI. **[Agent: cicd-github-actions]**
- [ ] Verify (induce drift): from SSM Session, `sudo sed -i 's/ListenPort = 51820/ListenPort = 51821/' /etc/wireguard/wg0.conf` (do NOT reload the service — kernel keeps old port). From operator laptop, click Refresh on the dashboard. Preview shows the drift banner; no Apply button. Revert the file edit; Refresh; banner gone, Apply button returns. **[Agent: wireguard-networking]**

---

## Slice 8 (optional — defer if v1 is shipping): "Pending in SSM" badge

**Outcome:** When a `terraform apply` has happened but no Refresh+Apply has run since, the dashboard surfaces a "Pending change in SSM" badge so the operator notices the new state needs to be applied.

- [ ] Reconciler computes a content hash (SHA-256 over the sorted client list) on every fetch; stores the last-applied hash in the DB. **[Agent: go-fullstack]**
- [ ] Snapshot endpoint (or a new `/api/reconcile/status`) returns `pending: bool` based on `current_ssm_hash != last_applied_hash`. **[Agent: go-fullstack]**
- [ ] Add a small badge on the clients card that renders when `pending == true`. **[Agent: go-fullstack]**
- [ ] Verify: TF apply with no-op change to force a hash update? Actually a no-op TF apply doesn't change the SSM value. Better test: edit `clients_config` to add a peer, apply, refresh the snapshot endpoint — badge should appear. Click Apply; badge disappears. **[Agent: go-fullstack]**

---

## Notes on agents and capabilities

All sub-tasks delegate to existing specialist agents — no new agent installation required:

| Agent | Used for |
|-------|----------|
| `terraform-aws` | TF resources (SSM param, IAM), HCL changes, plan/apply verification |
| `linux-cloud-init` | user-data heredocs, sudoers grants, systemd ordering, on-host operations |
| `go-fullstack` | Go packages (clientsource, reconcile, configfile, wgconf, handlers), htmx templates, SQLite migrations |
| `wireguard-networking` | `wg syncconf` invocation, peers file format, interface-drift parser, end-to-end VPN verification |
| `cicd-github-actions` | CI builds + SSM SendCommand deploys |
| `devsecops-quality` | `make pre-commit` runs after TF changes |
