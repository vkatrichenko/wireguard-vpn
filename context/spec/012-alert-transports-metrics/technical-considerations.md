# Technical Specification: Alert Transports & Prometheus Metrics

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Three mostly-independent workstreams in the Go dashboard, plus matching Terraform:

- **(A)** new alert transports behind the existing `notify.Notifier` interface, fanned out via a composite notifier;
- **(B)** a hand-rolled Prometheus `GET /metrics` handler reading a new in-memory poller snapshot (no Prometheus client dependency);
- **(C)** excision of the peer-down condition from `internal/alerts` plus its env/Terraform config.

The evaluator, the poller's off-critical-path dispatch, and the existing Slack incoming webhook (008, runtime-managed) are untouched in shape — every transport plugs into the same `Notify(ctx, message) error` seam. No new Go dependencies are introduced.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Transports (fan-out) — `internal/notify`

- Add a **`MultiNotifier`** implementing `Notifier`: holds `[]Notifier`, calls each in turn, **aggregates errors**, and **isolates failures** (one transport erroring never short-circuits the others). `main.go` builds the slice from env and wraps it; the poller still receives a single `Notifier`, so the evaluator/poller/dispatch are unchanged.
- New transports, each in its own file with an **immutable boot config** (no runtime holder — boot-config only per the functional spec), each a **no-op when unconfigured**:

  | Transport | Endpoint | Payload | Config (env) |
  |-----------|----------|---------|--------------|
  | Slack bot | `POST https://slack.com/api/chat.postMessage` (Authorization: Bearer) | `{channel, text}`; must check `ok` in the JSON response | `DASHBOARD_SLACK_BOT_TOKEN`, `DASHBOARD_SLACK_CHANNEL` |
  | Telegram | `POST https://api.telegram.org/bot<token>/sendMessage` | `{chat_id, text}` | `DASHBOARD_TELEGRAM_TOKEN`, `DASHBOARD_TELEGRAM_CHAT_ID` |
  | Discord | `POST <webhook url>` | `{content: text}` | `DASHBOARD_DISCORD_WEBHOOK_URL` |

- Factor the existing webhook sender's **post-JSON-with-timeout + bounded-retry** logic into a small shared helper reused by all transports. Reuse the existing `redactURL`/masking so tokens and URLs are never logged in full. The existing `Webhook` + `WebhookConfig` (with its 008 runtime set/test/revert) remain unchanged.

### 2.2 `/metrics` — `internal/server` + a poller seam

- Add `poller.MetricsSnapshot()` (mutex-guarded) returning the last-collected sample: service-active, peers (online/total, plus per-peer handshake age and cumulative rx/tx), and host cpu% / mem% / disk%. `collect()` already computes these each tick — store the last full sample in one struct under a new mutex and expose a deep-copy snapshot. **No extra `sudo`/`wg`/`/proc` exec and no DB query per scrape.**
- The server gains a nil-tolerant **`MetricsProvider`** interface (implemented by the poller), and reads the active-alert count from `StatusHolder.Snapshot()`. Register **`GET /metrics`** in the mux — distinct from the existing Chart.js `/api/metrics*` JSON endpoints.
- **Hand-rolled text exposition**, namespace `wireguard_`, each metric with `# HELP`/`# TYPE` and escaped label values:
  - `wireguard_service_active` (gauge, 0/1)
  - `wireguard_peers_total`, `wireguard_peers_online` (gauges)
  - `wireguard_peer_last_handshake_age_seconds{peer}` (gauge)
  - `wireguard_peer_rx_bytes_total{peer}`, `wireguard_peer_tx_bytes_total{peer}` (counters)
  - `wireguard_host_cpu_percent`, `wireguard_host_memory_percent`, `wireguard_host_disk_percent{mount}` (gauges)
  - `wireguard_active_alerts` (gauge), `wireguard_build_info{version,sha}` (gauge=1)
- Served on the existing VPN-bound `LISTEN_ADDR` listener, **no authentication** (VPN-gated like the rest).

### 2.3 Remove peer-down — `internal/alerts` + `main.go` + Terraform

- Delete `ConditionPeerDown`, `peerDownObservation()`, and its call site in `Evaluate` (**keep** the per-peer loop — the transfer-cap condition uses it); remove the `seenOnline` map and the `peerOnlineThreshold` / `peerStaleThreshold` fields plus `DefaultPeerStaleThreshold` / `DefaultPeerOnlineThreshold`. Drop `PeerStaleThreshold` from `alerts.Config` and the `envDuration("DASHBOARD_ALERT_PEER_STALE")` read in `main.go`. Remove the peer-down unit tests.
- **Terraform:** remove the `dashboard_alerts.peer_stale` field (variables.tf), its locals threading, and the `DASHBOARD_ALERT_PEER_STALE` line in `templates/user-data.txt`.

### 2.4 Terraform — new transport secrets (opt-in, mirrors the webhook)

- Per transport, a **count-gated `data.aws_ssm_parameter`** keyed on a new `*_param` variable, threaded into `alerts.env` only when set: `DASHBOARD_SLACK_BOT_TOKEN` / `DASHBOARD_SLACK_CHANNEL`, `DASHBOARD_TELEGRAM_TOKEN` / `DASHBOARD_TELEGRAM_CHAT_ID`, `DASHBOARD_DISCORD_WEBHOOK_URL`. Non-secret values (channel, chat ID) may be plain variables; secrets come from SSM. All default empty → **no behavior change when unconfigured**.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** the `notify.Notifier` interface + the poller's bounded-channel dispatch (007), the `StatusHolder`, and the SSM → `alerts.env` EnvironmentFile pattern (008). No new Go modules (hand-rolled exposition).
- **Potential Risks & Mitigations:**
  - **Slack `chat.postMessage` returns HTTP 200 with `{"ok":false}`.** → Parse the response body and treat `ok:false` (with its `error`) as a transport failure; do not assume 2xx means success.
  - **A single transport stalls dispatch.** → Per-transport request timeout + the existing off-critical-path worker; `MultiNotifier` isolates and aggregates failures.
  - **`/metrics` cardinality / data exposure.** → Labels bounded to the configured client count; the endpoint stays VPN-only and exposes only data the dashboard already shows.
  - **Removing `DASHBOARD_ALERT_PEER_STALE` from an existing deploy's env file.** → The binary ignores unknown env vars and Terraform stops emitting it; non-breaking.
  - **Secret leakage.** → Reuse the redaction helpers; never log tokens/URLs in full; secrets are SSM-seeded into a 0640 `alerts.env`, never committed.
  - **Alert egress vs the dashboard's "offline" guarantee.** → Transports are opt-in **outbound** egress, consistent with the 007 webhook; the dashboard's own operation stays offline. Grep guard: no new external URLs in web assets — only the three transport API hosts in Go code.

---

## 4. Testing Strategy

- **notify (unit):** per-transport table tests with an injected HTTP doer asserting URL / headers / payload (including Slack `ok:false` → error); a `MultiNotifier` test (one failing transport does not block the others; errors aggregated); redaction tests.
- **alerts (unit):** remove peer-down tests; add a regression asserting a stale/disconnected peer produces **no** alert; confirm the four remaining conditions (service-down, high-disk, sustained-CPU, transfer-cap) still fire and recover.
- **metrics (unit):** handler test over a fake snapshot asserting valid Prometheus exposition format, the key metric lines, and label escaping.
- **server:** `/metrics` route registered and served on the VPN listener.
- **Static / offline:** `gofmt` / `go vet` / `go test -race`; `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` build succeeds; grep web assets for external URLs (none new).
- **Terraform:** `terraform validate` + `plan` in `terraform/dev/` — confirm that removing `peer_stale` and adding the optional (empty-default) transport params produces no unexpected diff.
