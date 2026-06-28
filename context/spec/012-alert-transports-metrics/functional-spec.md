# Functional Specification: Alert Transports & Prometheus Metrics

- **Roadmap Item:** "Alerting & observability (Spec B)" (from _Future / Under Consideration_)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Today the dashboard pushes alerts to a single Slack-compatible incoming webhook (specs 007/008) and exposes metrics only as Chart.js JSON for its own UI. Two gaps follow: operators who don't use that webhook (or who want redundancy) can't get alerts in the channel they actually watch; and there is no way to pull the VPN's health into an external monitoring stack (Prometheus/Grafana) for richer dashboards, longer retention, or cross-host views.

Separately, the **peer-down alert has proven to be noise** — it fires whenever a client simply turns their VPN off, which is normal user behavior rather than an incident, and it trains the operator to ignore alerts.

This spec therefore:

1. Adds **Telegram, Discord, and a Slack bot** (`chat.postMessage`) as opt-in alert transports that fire **alongside** the existing Slack incoming webhook.
2. Adds a **Prometheus `/metrics`** endpoint so the VPN's current health can be scraped by an external monitoring stack.
3. **Removes the peer-down alert condition** so alerts reflect only real incidents.

**Success looks like:** an operator can receive the same alerts across any combination of the incoming webhook, a Slack bot, Telegram, and Discord (each independently opt-in); a Prometheus server on the VPN can scrape `/metrics` and graph peer/traffic/system/alert state; and disabling a client no longer generates an alert.

**Constraints carried over from 007/008 (unchanged):** cloud-agnostic (env/SSM config, no AWS SDK, no cloud lock-in); secrets never rendered or logged in full; alert dispatch stays off the poll critical path; everything remains **VPN-only** (no new public surface, no authentication).

---

## 2. Functional Requirements (The "What")

### 2.1 Additional alert transports (Slack bot, Telegram, Discord)

- **As the operator, I want** alerts delivered to a Slack bot, Telegram, and/or Discord in addition to the existing incoming webhook, **so that** I am notified in the channel(s) I actually use, with redundancy.
  - **Acceptance Criteria:**
    - [ ] A **Slack bot** transport posts alerts via the Slack Web API (`chat.postMessage`) to a configured channel when a **bot token + channel** are provided; it is a no-op when unconfigured. This is distinct from — and does not replace — the existing Slack **incoming webhook** transport (spec 008), which stays as-is including its runtime set/test/revert UI.
    - [ ] A **Telegram** transport delivers alert messages to a configured chat when a **bot token + chat ID** are provided; no-op when unconfigured.
    - [ ] A **Discord** transport delivers alert messages to a configured channel when an **incoming-webhook URL** is provided; no-op when unconfigured.
    - [ ] **Fan-out:** every configured transport fires for the **same** alert — with, e.g., the incoming webhook + Slack bot + Telegram all enabled, one service-down alert arrives in all three.
    - [ ] A single transport's failure (network, bad token, rate-limit) is logged and does **not** prevent the other transports from delivering, and does **not** block the poll loop (off-critical-path, same dispatch model as 007).
    - [ ] The edge-trigger / cooldown / recovery semantics (007) apply **uniformly** — all transports receive the same message stream; no transport has its own alerting logic.
    - [ ] Each new transport is **opt-in via env/SSM** configuration at boot; **no runtime management UI** is added for them. The existing incoming webhook keeps its 008 runtime set/test/revert.
    - [ ] All transport secrets (Slack bot token, Telegram bot token, Discord webhook URL) are **never logged or rendered in full** (redacted like the webhook), never committed, and supplied via the existing SSM → `alerts.env` EnvironmentFile pattern.

### 2.2 Prometheus `/metrics` endpoint

- **As the operator, I want** to scrape the VPN's health into Prometheus/Grafana, **so that** I can build external dashboards, longer-retention graphs, and my own alerting.
  - **Acceptance Criteria:**
    - [ ] `GET /metrics` returns current metrics in the **Prometheus text exposition format**, served on the existing VPN-bound listener (no new port, no authentication — VPN-gated like the rest of the dashboard).
    - [ ] Exposed metrics cover at least: WireGuard service active (0/1), peers total and online, per-peer last-handshake age and cumulative rx/tx bytes, host CPU% / memory% / disk%, and active-alert count. (Exact metric names and labels are finalized in the technical spec.)
    - [ ] Metrics reflect **current** values read from already-collected in-memory state — **no** extra `sudo`/`wg`/`/proc` exec and **no** per-scrape SQLite query; a scrape never disrupts the poll loop or the dashboard.
    - [ ] Per-peer series are labeled by peer name; cardinality stays bounded to the configured client count.
    - [ ] `/metrics` is documented (README / architecture) as VPN-only, making no outbound requests, and exposing only data the dashboard already shows.

### 2.3 Remove the peer-down alert

- **As the operator, I want** alerts to reflect real incidents only, **so that** I am not paged when someone simply turns their VPN off.
  - **Acceptance Criteria:**
    - [ ] The **peer-down / stale-peer** alert condition no longer exists: turning a client off (or a client going idle) produces **no** alert and **no** Events entry.
    - [ ] The `DASHBOARD_ALERT_PEER_STALE` env var and the corresponding Terraform `dashboard_alerts.peer_stale` variable + its `alerts.env` line are removed; their absence is **not** a breaking configuration error.
    - [ ] The remaining **four** conditions (service-down, high-disk, sustained-CPU, per-peer transfer-cap) are unchanged and still fire and recover correctly.
    - [ ] Docs (architecture §8, product-definition) are updated from "five conditions / a dropped peer" to the four-condition set.

---

## 3. Scope and Boundaries

### In-Scope

- **Slack bot, Telegram, and Discord** transports behind the existing `Notifier` interface.
- **Fan-out** delivery to all configured transports; opt-in env/SSM config; secret redaction.
- The **Prometheus `/metrics`** endpoint (text format, current gauges, VPN-only, no auth).
- **Removal** of the peer-down condition + its env/Terraform config + documentation updates.
- **Terraform** wiring: new opt-in SSM-seeded transport secrets (Slack bot token + channel, Telegram token + chat ID, Discord webhook URL); removal of `peer_stale`.

### Out-of-Scope

- **Email / SMTP / SES alerting** — explicitly excluded this version (owner decision).
- **Runtime management UI (set / test / revert) for the new transports** — boot-config only; the incoming webhook's 008 runtime UI is unchanged and **not** extended to the new transports.
- **Authentication on `/metrics` or the dashboard** — VPN gating only (unchanged non-goal).
- **Historical data in `/metrics`** — Prometheus stores history itself; the endpoint exposes **current** values only. The existing Chart.js `/api/metrics*` endpoints are unchanged.
- **PagerDuty / SMS / other transports**, and **per-transport message templating / rich formatting** beyond the shared plain-text message.
- **Spec C — ARM option** (Graviton arm64) — separate specification.

> **Product-definition note:** this spec deliberately reverses `product-definition.md` §3.2's "single Slack-compatible webhook only (no email/SMS/PagerDuty routing)" non-goal for the chat transports and its implicit external-metrics exclusion. `product-definition.md` will be updated at verify time to reflect multi-transport alerting + the metrics endpoint (still no email/SMS/PagerDuty, still no auth).
