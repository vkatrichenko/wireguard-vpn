# Technical Specification: Alert Notifications

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Draft
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

Add an **alert evaluator** to the dashboard's existing poll loop and an **outbound webhook notifier**. The evaluator reads state the dashboard already gathers each tick (systemd status, disk, CPU, per-peer handshake recency), runs a per-condition state machine (edge-trigger → cooldown → recovery), and hands fired alerts to the notifier, which POSTs a Slack-compatible payload.

The only infrastructure question is **where the webhook URL (a secret) lives**. Recommended: an **SSM SecureString parameter**, mirroring how the server's WireGuard private key is already handled — read at boot, injected as an environment variable into the systemd unit. This keeps the secret out of the repo and out of plaintext user-data. Outbound egress is already open (`sg.tf` egress `0.0.0.0/0`), so no security-group change is needed.

This is the one feature that adds **outbound** behavior; it adds **no inbound** surface and no auth change.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Configuration / Secret Handling

| Option | Mechanism | Trade-off |
|--------|-----------|-----------|
| **A — SSM SecureString** *(recommended)* | New param e.g. `/config/wireguard-vpn-test/dashboard-webhook-url`; instance reads it at boot (KMS-decrypt) and sets it as a systemd `Environment=`/`EnvironmentFile=` value. | Consistent with the existing server-key pattern; secret never in the repo or plaintext user-data. Cost: a small instance IAM grant (`ssm:GetParameter` + KMS decrypt) on that one path. Created out-of-band like the server key. |
| **B — Terraform variable → systemd env** | Operator sets a TF variable; rendered into the unit/user-data. | No new IAM, but the URL lands in base64 user-data (readable via instance-metadata/IAM) and risks being committed. Weaker secret hygiene. |

Recommendation: **A**. Mark for owner confirmation; the parameter is created manually (like the server key) and is **not** managed by Terraform (consistent with the repo's "external state" note).

Other config (thresholds, windows, cooldown) are plain (non-secret) environment variables on the unit with the documented defaults, so tuning needs no code change.

### Architecture / Component Breakdown

| Component | Path | Responsibility |
|-----------|------|----------------|
| `alerts` (new) | `dashboard/internal/alerts/` | Per-condition state machine: evaluate(samples) → fired/recovered events; owns thresholds, sustain window, cooldown, edge-trigger, re-arm. Pure given inputs + a clock — fully unit-testable. |
| Notifier (new) | `dashboard/internal/alerts/` (or `internal/notify/`) | Build the Slack-compatible JSON payload and POST it with timeout + bounded backoff retry. Isolated behind an interface so tests inject a fake transport. |
| Poller (extend) | `dashboard/internal/poller/` | On each existing tick, pass the already-collected service/disk/CPU/peer state to the evaluator; dispatch resulting events to the notifier. Delivery runs off the critical path so a slow webhook never blocks polling. |
| Config (extend) | `dashboard/cmd/wireguard-dashboard/main.go` | Read `DASHBOARD_WEBHOOK_URL` (+ threshold envs) at startup; if the URL is empty, the evaluator still runs for the in-UI view but the notifier is a no-op (opt-in per functional spec §2.2). |
| Active-alerts view (extend) | `dashboard/web/templates/...` (Overview/Events) | Render currently-active alerts + "alerting enabled/disabled" status. No new tab. |

### Logic / Algorithm — per-condition state machine

- States per condition: `OK` ↔ `FIRING`. Transition `OK→FIRING` emits an alert; `FIRING→OK` emits a recovery. While `FIRING`, suppress re-notification until the cooldown elapses (default 30 min).
- **Sustained CPU** uses a rolling check over the sustain window (default 5 min) so spikes don't fire — reuse the CPU samples already in the store/poller rather than re-reading `/proc`.
- **Peer-down** is keyed per client; a client must have been seen online at least once this run before it can fire (avoids alerting on never-connected peers) — see functional-spec `[NEEDS CLARIFICATION]`.
- State is **in-memory**; on restart, conditions re-arm from current state (a still-bad condition may notify once post-restart — accepted in §2.3). No alert DB.
- The evaluator takes an injected clock so cooldown/sustain logic is deterministically testable.

### API / Payload

- **Outbound only:** HTTPS POST to the configured webhook with a Slack-compatible JSON body (`{"text": "..."}` style), message including condition, measured value, host/instance id (from IMDSv2), and timestamp.
- **No new inbound route** is required; the in-UI active-alerts view is served via the existing `/partial/...` + `/api/...` conventions (e.g. a small `/api/alerts` returning current state).

### Terraform / Infra (minimal)

- **If Option A:** add an instance IAM statement granting read on the one SSM parameter (+ KMS decrypt), mirroring the server-key grant; thread the param name + threshold env values into the systemd unit via user-data. The parameter itself is created manually (external state).
- **No security-group change** (egress already open). No inbound change.

---

## 3. Impact and Risk Analysis

- **System Dependencies:** existing poller/store (CPU, disk, peer handshakes), systemd status reader, IMDSv2 (host id in messages). Option A adds a dependency on one SSM parameter + KMS. Outbound internet (already available — the host is the VPN exit node).
- **Potential Risks & Mitigations:**
  - **Alert spam.** *Mitigation:* edge-trigger + per-condition cooldown + single recovery message; unit-tested around the transitions.
  - **Webhook slowness blocking polling.** *Mitigation:* deliver asynchronously with a timeout + bounded backoff; the poll loop never waits on the HTTP call.
  - **Secret leakage.** *Mitigation:* Option A (SSM SecureString); never log the full URL (redact), never render it in the UI, never commit it.
  - **Restart re-notification.** *Mitigation:* documented and accepted; re-arm from current state, no historical replay.
  - **Flapping conditions.** *Mitigation:* sustain window for CPU, stale threshold for peers, cooldown for all — tune via env without code changes.
  - **False "peer down" for intermittent clients.** *Mitigation:* require prior online state this run; revisit opt-in subset if noise persists (open question in §2.1).

---

## 4. Testing Strategy

- **Unit (core):** drive the state machine with an injected clock and synthetic sample sequences — assert exactly one alert on entry, suppression during cooldown, exactly one recovery on clear, correct re-arm, sustained-CPU ignoring spikes, and per-condition/per-client isolation.
- **Notifier:** inject a fake HTTP transport — assert payload shape, 2xx success, retry/backoff on non-2xx/timeout, and that the URL is redacted in logs. Verify no-op behavior when the URL is unset.
- **Poller integration:** with fakes, confirm a simulated service-down/disk-full/peer-stale flows from evaluate → notifier without blocking the tick.
- **Handler test:** `/api/alerts` reflects active state (firing vs. ok, enabled vs. disabled).
- **Manual end-to-end:** point at a real Slack test webhook; trip each condition (stop `wg-quick`, fill a tmp filesystem, busy-loop CPU, idle a peer) and confirm fire + recovery arrive and spam is suppressed. A passing build does not prove delivery — the webhook receipt must be observed.
- **Quality gate:** `make test` in `dashboard/`; `make pre-commit` at repo root if Option A adds Terraform/IAM.
