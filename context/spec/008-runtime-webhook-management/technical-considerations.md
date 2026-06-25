# Technical Specification: Runtime Webhook Management

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Spec 007 wired alert delivery as a **fixed-at-boot** value: `cmd/wireguard-dashboard/main.go` reads `DASHBOARD_WEBHOOK_URL` once and constructs either a `*notify.Webhook` (fixed URL) or a `notify.NoOp`, handing the resulting `notify.Notifier` to the poller's dispatch loop. To make the webhook **runtime-mutable** without persistence, we invert that: introduce a small thread-safe **`WebhookConfig` holder** that owns the boot **seed** (the env value) plus an optional in-memory **override**, and make the notifier resolve the target URL from the holder **at send time**. Setting/reverting from the UI mutates the holder; the next dispatch (or a test send) picks up the change immediately. A restart rebuilds the holder from env, discarding any override — so the secret is never persisted and env/SSM stays authoritative at boot (functional spec §2.4).

This adds the dashboard's first **write endpoints** (`POST /api/webhook`, `/test`, `/revert`) and a small About-tab card. It also lands the **deploy-time seed** that 007 deferred: the Terraform wiring that reads the webhook URL from SSM and renders it (plus the 007 alert env knobs) into a systemd `EnvironmentFile` on the host.

Two layers, independently shippable: **(A)** the Go runtime-management feature (holder + endpoints + UI), and **(B)** the Terraform boot-seed wiring. (A) works with any env-provided seed; (B) is what makes a real deployment’s seed come from SSM.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Architecture / Component Breakdown

| Component | Path | Responsibility |
|-----------|------|----------------|
| `WebhookConfig` holder (new) | `dashboard/internal/notify/` | Thread-safe (`sync.RWMutex`) owner of `seed` (immutable env value) + `override *string`. Methods: `Current() string` (override→seed), `Set(url)`, `Revert()`, `Enabled() bool`, `Status() Status{Enabled, MaskedURL, OverrideActive}`. `MaskedURL` reuses the existing `redactURL` (scheme+host). The single source of truth for "where do we POST". |
| Notifier (refactor) | `dashboard/internal/notify/` | The Slack sender resolves its URL from the holder per call instead of a fixed `Webhook.URL`. `Notify(ctx, msg)` reads `cfg.Current()`; empty → no-op (returns nil, no HTTP); non-empty → POST with the existing timeout + bounded-retry + redacted-logging machinery. Replaces the static `Webhook`/`NoOp` split in the dispatch path. |
| Config seed (refactor) | `dashboard/cmd/wireguard-dashboard/main.go` | At startup read `DASHBOARD_WEBHOOK_URL` and construct `notify.NewWebhookConfig(seed)`; build the holder-backed notifier; pass the holder to BOTH the poller (delivery) and the server (status + write endpoints). Removes the one-shot `statusHolder.SetEnabled(...)`. |
| Webhook HTTP handlers (new) | `dashboard/internal/server/handlers_webhook.go` | `GET /api/webhook` (status), `POST /api/webhook` (set), `POST /api/webhook/test` (test send), `POST /api/webhook/revert` (revert). First non-GET routes. |
| Alerts status (small change) | `dashboard/internal/alerts/` + `internal/server` | `enabled` becomes **dynamic**: the alert status render + `/api/alerts` read `webhookCfg.Enabled()` at request time instead of the cached bool from 007 Slice 5. |
| About-tab card (new) | `dashboard/web/templates/tabs/about.html` (+ a `cards/webhook.html` fragment) | Masked current value + enabled/override status + edit field + **Set** / **Test** / **Revert** controls (htmx POSTs). Never renders the full secret. |
| Terraform boot-seed (new) | `terraform/modules/wireguard/*` + `terraform/dev/main.tf` | SSM→`EnvironmentFile` wiring (see Terraform section). |

### Logic — the holder & dynamic resolution

- `Current()` returns `*override` when set, else `seed`. `Set(url)` stores the override; `Revert()` clears it. All guarded by an `RWMutex` (dispatch + test reads are frequent, writes rare).
- The notifier is **always present** (no `NoOp` swap): when `Current()` is empty it is a silent no-op, so the evaluator/dispatch path is unconditional and the in-UI alert view (007 §2.4) still runs. `Enabled()` is `Current() != ""`.
- **No poller change** — the poller already calls `Notifier.Notify(ctx, msg)`; the notifier now resolves the URL itself. The off-critical-path dispatch (007 Slice 2) is unchanged.
- **No persistence** — the override lives only in the holder’s memory; nothing is written to SQLite or disk. Restart rebuilds from `seed`.

### API Contracts

| Method / Path | Request | Response | Notes |
|---|---|---|---|
| `GET /api/webhook` | — | `{enabled: bool, current_masked: string, override_active: bool}` | `current_masked` = scheme+host or `""`; never the full URL. |
| `POST /api/webhook` | `{url: string}` (JSON or form) | `200` + status body; `400` `{error}` on invalid | Validate **well-formed `https://`** URL; reject other schemes/malformed; previous value unchanged on reject. |
| `POST /api/webhook/test` | — | `200 {delivered: bool, reason?: string}` | Sends a canned test message to `Current()` via the same delivery path. `409`/`{delivered:false}` when no URL configured. Never echoes the URL. |
| `POST /api/webhook/revert` | — | `200` + status body | Clears the override → back to `seed` (or disabled if none). |

- Routes register in `internal/server/server.go` alongside the existing `mux.HandleFunc("GET ...")` block. These are the first `POST` routes; Go's `net/http` mux matches on method so no conflict.
- The About partial (`/partial/about`) renders the card server-side from `webhookCfg.Status()`; each action returns the updated card fragment (htmx swap) so the masked status refreshes without a full reload.

### Configuration / Secret Handling — Terraform boot-seed (layer B)

Mirrors the **existing server-private-key pattern** (Terraform reads SSM via a data source and templates it into user-data — see `modules/wireguard/datasource.tf` + `locals.tf`). **No instance IAM grant** (Terraform reads SSM at apply with the operator’s creds); the parameter is created **out-of-band** (external state), like `/config/<project>-<env>/default-private-key`.

| File | Change |
|---|---|
| `modules/wireguard/variables.tf` | Add `dashboard_webhook_url_param` (string, default `""` = alerting seed off) + a typed `dashboard_alerts` object with `optional()` fields for the 007 knobs (`host_label`, `disk_pct`, `cpu_pct`, `cpu_sustain`, `peer_stale`, `transfer_bytes`) defaulting to the documented values. |
| `modules/wireguard/datasource.tf` | Add `count`-gated `data "aws_ssm_parameter" "dashboard_webhook_url"` (`with_decryption = true`, only when the param name is non-empty). |
| `modules/wireguard/locals.tf` | Thread the webhook value (or `""`) + alert knobs into the `templatefile()` call. |
| `modules/wireguard/templates/user-data.txt` | In the dashboard block, render `/etc/wireguard-dashboard/alerts.env` (`0640 root:wireguard-dashboard`, like `clients.json`) with the non-secret knobs always and a `DASHBOARD_WEBHOOK_URL=` line only when set; add `EnvironmentFile=-/etc/wireguard-dashboard/alerts.env` to the unit’s `[Service]` block (`-` makes it optional → opt-in). |
| `terraform/dev/main.tf` | Pass the new inputs; default `dashboard_webhook_url_param = ""` (wired-but-disabled — enable later by creating the SSM param and setting the name). |

The webhook line is rendered inside the existing quoted heredoc convention so Terraform substitutes the value at apply while bash performs no re-expansion (same handling as `clients_json`). The secret lands in user-data base64 — the accepted posture the far-more-sensitive server private key already uses.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** the 007 `notify` sender + poller dispatch loop (refactored to holder-resolved URL), the 007 Slice-5 alert status/`enabled` rendering, the server mux + About tab, and (layer B) the existing SSM server-key data-source pattern + user-data systemd unit.
- **Potential Risks & Mitigations:**
  - **Concurrency.** The holder is read by the dispatch goroutine + test sends and written by UI request goroutines. *Mitigation:* `sync.RWMutex`; a `-race` test exercising reads during writes; `Status()`/`Current()` return values (no shared references).
  - **Secret leakage.** *Mitigation:* mask to scheme+host in the UI and `/api/webhook`; never log the full URL (reuse `redactURL`); never persist (in-memory only); reject non-https; validate before storing.
  - **No-auth write surface (posture change).** The first write endpoints on a no-auth dashboard — any VPN peer can set/redirect/test/revert. *Mitigation:* bounded to **outbound webhook target only** (no inbound listener, no other control op); documented as a conscious solo-VPN trade-off (§2.5); revisit if multi-user.
  - **Test-send abuse.** Any peer can trigger outbound POSTs via `/test`. *Mitigation:* low impact for a solo VPN; the canned message is harmless; consider a trivial in-process throttle if it ever matters (not in v1).
  - **CSRF.** No auth + state-changing POSTs. *Mitigation:* same-origin htmx form on a VPN-only origin; v1 ships **no CSRF token** (documented); acceptable given no auth/session to forge against. Revisit with any future auth.
  - **Override lost on restart.** *Mitigation:* by design (§2.4) — env/SSM re-seeds; documented in the UI copy ("resets to deploy value on restart").
  - **Terraform `ignore_changes = [user_data]`.** The instance ignores user_data changes, so the new `EnvironmentFile` wiring only takes effect on the **next instance replacement** (release-tag bump + taint, or AMI change). *Mitigation:* call this out for the owner’s apply; the seed isn’t live until the instance rolls.

---

## 4. Testing Strategy

- **Unit — holder (`internal/notify`):** `Set`/`Revert`/`Current`/`Enabled`/`Status` correctness; masking; `-race` with concurrent readers (dispatch/test) vs a writer (UI).
- **Unit — notifier:** resolves URL from the holder per send; no-op (no HTTP) when `Current()` empty; POSTs to the override after `Set`; reverts to seed after `Revert`; URL still redacted in logs.
- **Handler tests (`httptest`):** `GET /api/webhook` returns masked status (enabled/override flags); `POST /api/webhook` accepts a valid https URL (200 + effect) and rejects non-https/malformed (400, previous value intact); `POST /api/webhook/test` reports delivered/failed via an injected fake transport and handles "no URL configured"; `POST /api/webhook/revert` restores the seed. Assert **no full URL** appears in any response or log.
- **Render test:** the About card shows the masked value, the enabled/override states, and the Set/Test/Revert controls; assert the full secret never appears in the markup.
- **Regression:** 007 alert delivery still works through the refactored notifier; `/api/alerts` `enabled` now tracks the live holder; `go test -race ./internal/notify ./internal/poller ./internal/server` green.
- **Terraform (layer B):** `terraform fmt`/`validate` + `make pre-commit` (tflint/trivy); the owner reviews `terraform plan` before apply (the `ignore_changes` note means a deliberate instance replacement is needed to apply the seed). Not unit-tested in the Go suite.
- **Manual E2E:** with a real Slack-compatible test webhook — set via the About card, hit **Test** and confirm receipt, trip a real condition and confirm delivery to the new target, **Revert** and confirm it falls back, restart and confirm the env/SSM seed returns. (Pairs with 007’s manual Slice 6.)
