# Tasks: First-Client Onboarding & Dashboard Usability Fixes

> **Verification reality:** No browser MCP — UI changes are verified via Go handler/template tests (the `server_test` harness), not a live browser. Live `wg` / `systemctl` / full VPS lifecycle behavior is **owner-run** (CLAUDE.md), collected into the final slice.
>
> **Per-slice gate:** dashboard slices end green on `cd dashboard && go build ./... && go vet ./... && go test ./...` (+ static `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` build). install.sh slices end green on `make shellcheck`. Each slice leaves the app/script runnable.

---

### Slice 1 — Example client config in install output (req 2.1)

- [x] Add `emit_example_client_config()` to `scripts/install.sh`: derive the first-client IP from `WG_SERVER_NET` (server host + 1 → `/32`) and print a `wg-quick` `[Interface]`/`[Peer]` template using `SERVER_PUBLIC_KEY`, `WG_SERVER_PORT`, `${WG_CLIENT_DNS}`, an `<server-public-ip>:port` endpoint placeholder, full-tunnel `AllowedIPs` (split noted inline), and a `PrivateKey` placeholder + off-host keygen/register hint. No keys generated. **[Agent: linux-cloud-init]**
- [x] Call it from the success summary on **both** WG-only and dashboard paths, after the "WireGuard server is up" block. **[Agent: linux-cloud-init]**
- [x] Verify: `make shellcheck` exit 0; render the helper output for the default subnet (e.g. invoke under a stub shell or grep the emitted block) and confirm `172.16.15.2/32` + real server pubkey appear; confirm a custom `WG_SERVER_NET` yields the right first IP. Live install print → Slice 5. **[Agent: linux-cloud-init]** _(verified 2026-06-30: shellcheck exit 0; renders 172.16.15.2/32 for default, 10.8.0.2/32 for custom subnet.)_

### Slice 2 — Handshakes: real names, one row per peer (req 2.3)

- [x] `internal/db/db.go`: add `QueryLatestHandshakePerPeer(ctx, from, to, limit)` → one `HandshakeEvent` per `public_key` (`MAX(ts)`), newest-first. **[Agent: go-fullstack]**
- [x] `internal/server/handlers_partial_tabs.go` (events handler): resolve each row's display name via a `public_key→name` map from `clientsSvc.List(ctx)`; unmatched key → shortened key + "unknown". View-model row carries `{TS, Name, Unknown}`; switch the query to the per-peer one. **[Agent: go-fullstack]**
- [x] `web/templates/cards/events.html`: render the resolved name (optional styling for the unknown case). **[Agent: go-fullstack]**
- [x] Verify: `db` test (dedupe to one row per peer + newest-first ordering, in-memory); events-handler test (seeded clients DB → names; unmatched → "unknown"; deduped). Full `go test ./...` + build green. **[Agent: go-fullstack]** _(verified 2026-06-30: build/vet/test + static build green.)_

### Slice 3 — Inline client editing (req 2.2)

- [x] `web/templates/tabs/clients.html`: replace the `<details class="client-edit">` popover with a full-width `<tr class="client-edit-row hidden">` (colspan) holding the existing edit form (same fields, same `hx-patch="/api/clients/{name}"` / `hx-target="#clients"`); Edit button toggles it, mirroring the `detail-row hidden` pattern. **[Agent: go-fullstack]** _(toggle via delegated handler in `web/static/tabs.js`; Cancel added.)_
- [x] `web/static/app.css`: remove the absolute right-drawer rules; lay the edit fields out full-width/responsive. **[Agent: go-fullstack]**
- [x] Verify: handler/template test that the inline edit row renders the form with the correct PATCH target and fields; existing clients-card + PATCH tests stay green; full `go test ./...` + build green. **[Agent: go-fullstack]** _(verified 2026-06-30: build/vet/test + static build green.)_

### Slice 4 — install.sh lifecycle: safe update + remove/purge (req 2.4)

- [x] `scripts/install.sh`: add arg parsing (`--uninstall`, `--purge`, `--dashboard-only`; no-args ⇒ install/update; unknown ⇒ usage error) resolving to `ACTION` + `PURGE`/`DASHBOARD_ONLY`; keep the EC2 no-arg invocation intact. **[Agent: linux-cloud-init]**
- [x] Safe-update path: when `wg0.conf` exists, rewrite only `[Interface]` and preserve on-disk `[Peer]` stanzas (awk-merge like `wg-sync`), apply via `wg syncconf` (no tunnel drop); fresh install (no `wg0.conf`) keeps today's `WG_PEERS` + `enable --now` behavior. **[Agent: linux-cloud-init]**
- [x] `remove()`: idempotent teardown — dashboard always (service/unit/`wg-sync`/sudoers/binary/user), `wg-quick@wg0` unless `--dashboard-only`; keep `/etc/wireguard` + DB unless `--purge` (which also deletes `wg0.conf`, `server.key`, DB dir, `/etc/wireguard-dashboard`). **[Agent: linux-cloud-init]**
- [x] Verify: `make shellcheck` exit 0 across default / `--uninstall` / `--purge` / `--dashboard-only`; static review of the merge + guards. Live behavior → Slice 5. **[Agent: linux-cloud-init]** _(verified 2026-06-30: shellcheck exit 0; diff reviewed — dispatch, safe-update merge, remove/purge guards.)_

### Slice 5 — Owner-run end-to-end validation (cannot be done in-session)

- [x] **Owner-run** on a real Ubuntu VPS: fresh install prints the example config; add a client via the UI (inline edit works; handshakes show the name, one row per peer); rerun-update preserves peers, doesn't drop the tunnel, runs the new binary; `--uninstall` removes services but keeps data; reinstall keeps the same server identity; `--purge` wipes; `--dashboard-only` leaves the VPN up. EC2: confirm the rendered user-data path is behavior-unchanged. **(owner)** _(owner-verified deployed & all functionality working 2026-06-30; required the post-v0.0.10 fixes — capture-phase edit toggle + server-key persistence, PR #48.)_

---

### Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slices 2–3 UI verify | No browser MCP | htmx fragments asserted in Go handler/template tests, not a live browser |
| Slices 1, 4, 5 | Live `wg`/`systemctl`/VPS lifecycle can't run in-session (macOS dev host) | Agents do shellcheck + static review; owner runs the live lifecycle in Slice 5 |
