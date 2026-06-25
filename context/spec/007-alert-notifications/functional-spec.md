# Functional Specification: Alert Notifications

- **Roadmap Item:** Not yet on the roadmap (proactive monitoring follow-on to 003/006)
- **Status:** Completed
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The dashboard is **pull-only**: it shows problems accurately, but only when the operator happens to look. For a solo-maintained VPN that's the wrong failure mode — the service can be down, the disk can fill, or a peer can drop for hours before anyone notices, because nobody is watching a tunnel-only dashboard at 2am.

The dashboard already evaluates everything an alert needs on its existing poll loop: service state (`wg-quick@wg0`), CPU and disk, and per-peer handshake recency. This spec adds a **push**: when a watched condition crosses a threshold, the dashboard sends an outbound notification to a **Slack-compatible webhook**, and sends a recovery notification when it clears.

Webhook delivery is chosen over email specifically to avoid standing up SES/SMTP on the host; the instance already has open outbound egress, so a single HTTPS POST is the cheapest reliable channel.

**Success looks like:** the operator finds out the service went down, the disk is filling, or a key peer dropped — within a poll interval, in their chat — without watching the dashboard, and gets a clear "recovered" message when it's back, with no alert spam in between.

---

## 2. Functional Requirements (The "What")

### 2.1 Watched conditions

- **As the operator, I want** the dashboard to watch for the failures that actually matter, **so that I'm** told before they become incidents.
  - **Acceptance Criteria:**
    - [x] **Service down:** `wg-quick@wg0` is not active → alert.
    - [x] **High disk:** any monitored filesystem ≥ a disk threshold (default 90%) → alert.
    - [x] **High CPU sustained:** CPU ≥ a CPU threshold (default 90%) continuously for a sustain window (default 5 minutes) → alert (a brief spike must not fire).
    - [x] **Peer down:** a client that was online goes stale beyond the peer-stale threshold (default 10 minutes) → alert. **Resolved:** fires only after the peer has been seen online at least once this run (the seen-online-once gate) — never-connected peers never alert. (Operator-evidenced at verify: a real peer-down alert delivered to Slack.)
    - [x] Thresholds and windows are configurable with the documented defaults above.
    - [x] **Implemented addition (recorded at verify):** a fifth condition — **per-peer cumulative-transfer cap** (a client's Rx or Tx since dashboard start crosses a configurable limit, default 50 GiB) — ships alongside the four above, with the same edge-trigger/cooldown semantics (recovery only on a counter reset). All thresholds are configured **env-var-only** (`DASHBOARD_ALERT_*`), seeded via Terraform's SSM→`EnvironmentFile` wiring (spec 008), not an in-process SSM read.

### 2.2 Delivery

- **As the operator, I want** alerts in my chat, **so that I** see them without opening the dashboard.
  - **Acceptance Criteria:**
    - [x] Each alert is sent as an HTTPS POST to a configured **Slack-compatible incoming-webhook URL** with a human-readable message (condition, value, host, timestamp).
    - [x] If no webhook URL is configured, alerting is **disabled silently** (the dashboard runs exactly as today) — the feature is opt-in.
    - [x] A delivery failure (non-2xx / timeout) is logged and retried with bounded backoff; it never crashes the dashboard or blocks the poll loop.

### 2.3 Edge-triggering, cooldown & recovery

- **As the operator, I want** one alert per problem, not a flood, **so that** notifications stay meaningful.
  - **Acceptance Criteria:**
    - [x] An alert fires on the **transition** into the bad state, not on every poll while it persists (edge-triggered).
    - [x] While a condition stays bad, **re-notification is suppressed** for at least a cooldown window (default 30 minutes).
    - [x] When a condition returns to normal, a single **recovery** notification is sent and the condition re-arms.
    - [x] Alert state is per-condition (and per-client for peer-down); one noisy condition doesn't suppress others.
    - [x] After a dashboard restart, conditions re-arm from current state (no replay of historical alerts); a still-bad condition may re-notify once after restart — acceptable.

### 2.4 Visibility in the UI

- **As the operator, I want to** see current alert status in the dashboard, **so that** the push and the pull views agree.
  - **Acceptance Criteria:**
    - [x] The dashboard surfaces currently-active alerts (e.g. a small banner or an Events/About entry) and whether alerting is configured/enabled.
    - [x] No new tab is required; reuse the existing Events/Overview surfaces. **Resolved:** an active-alerts strip on Overview + alert entries in Events.

### 2.5 Security & access (unchanged inbound)

- **As the operator, I want** alerting to add no inbound exposure, **so that** the security posture is unchanged except for the outbound webhook.
  - **Acceptance Criteria:**
    - [x] The webhook URL is treated as a **secret** — never rendered into the dashboard UI, never logged in full, never committed to the repo.
    - [x] Alerting adds only **outbound** HTTPS; no new inbound listener, no in-band auth change (still VPN-gated `http://172.16.15.1:8080`).
    - [x] Alert messages contain operational data only (condition, host id, metric value) — no secrets, no client private data beyond the client name already shown in the UI.

---

## 3. Scope and Boundaries

### In-Scope

- Four watched conditions: service-down, high-disk, sustained-high-CPU, peer-down — with configurable thresholds/defaults.
- Outbound delivery to a Slack-compatible incoming webhook, opt-in (disabled when unset), with bounded retry.
- Edge-triggering, per-condition cooldown, and recovery notifications.
- A lightweight in-UI view of currently-active alerts and whether alerting is enabled.

### Out-of-Scope

- **Email / SMS / PagerDuty / generic multi-channel routing** — single Slack-compatible webhook only for v1.
- **Per-condition custom message templating / alert rules engine** — fixed conditions with configurable thresholds only.
- **Historical alert log / acknowledgement workflow** — no alert database, no ack/silence UI beyond cooldown.
- **Alerting on connection-history analytics** (e.g. "client connected from a new country") — possible later; depends on 006.
- **Inbound webhooks / receiving commands** — outbound only.
- **Any change to the read-only, VPN-only, no-inbound-auth posture.**
- **Config download (004), binary distribution (005), connection-history/geo-map (006)** — separate specifications.
