# Functional Specification: Runtime Webhook Management

- **Roadmap Item:** Not yet on the roadmap (operability follow-on to 007-alert-notifications)
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

Spec 007 added alert notifications: the dashboard evaluates watched conditions and, when a Slack-compatible webhook URL is configured via the `DASHBOARD_WEBHOOK_URL` environment variable (fed from SSM at deploy time), POSTs alerts to that webhook. Today that URL can only be **changed by editing infrastructure** — updating the SSM parameter and rolling the instance, or hand-editing the host's environment file and restarting the service.

For a solo-operator VPN that's friction at exactly the wrong moment: when you're trying to get alerts flowing to a new channel, point them at a throwaway test webhook, or silence a misrouted integration, you don't want to redeploy. This spec lets the operator **manage the webhook URL from the dashboard UI at runtime** — set it, send a test alert to confirm delivery, and revert to the deploy-time value — without touching Terraform or restarting the service.

Crucially, this is a **runtime override**, not new persistent configuration. The environment/SSM value remains the authoritative source at every boot; a UI change applies immediately to live delivery but is **held in memory only** and is discarded on restart, when the env/SSM value re-seeds. The webhook secret is therefore never written to the dashboard's database or disk — its only durable home stays SSM.

This is also the dashboard's **first write operation**. Specs 002/003/006/007 were strictly read-only. Adding a write path on a VPN-gated, **no-authentication** dashboard is a deliberate, scoped exception to that posture, and §2.4 records the trade-off explicitly.

**Success looks like:** the operator can change where alerts go, prove delivery with a test message, and roll back — all from the Overview/About surface, in seconds, with the running service — while the durable configuration story (SSM → env → boot) is unchanged and the secret never lands on disk.

---

## 2. Functional Requirements (The "What")

### 2.1 View current webhook status

- **As the operator, I want to** see whether alerting is configured and where it points, **so that I can** confirm the current delivery target at a glance.
  - **Acceptance Criteria:**
    - [ ] The UI shows whether alert delivery is currently **enabled** (a webhook URL is set) or **disabled** (none set).
    - [ ] When a webhook is set, the UI shows it **masked** to scheme + host only (e.g. `https://hooks.slack.com/****`); the secret path/token is never rendered.
    - [ ] The UI indicates whether the current value is a **runtime override** (set via the UI this session) or the **deploy-time value** (from env/SSM).

### 2.2 Set / replace the webhook at runtime

- **As the operator, I want to** set or replace the webhook URL from the UI, **so that I can** redirect alerts without redeploying.
  - **Acceptance Criteria:**
    - [ ] The operator can submit a new webhook URL from the UI; on success it takes effect **immediately** for subsequent alert deliveries (no restart).
    - [ ] Submitting a webhook when none was configured flips delivery to **enabled**; the in-UI alert view (007 §2.4) reflects the new enabled state.
    - [ ] A submitted value must be a **well-formed `https://` URL**; any other scheme or a malformed value is rejected with a clear message and the previous value is unchanged. (https-only: every Slack-compatible target — Slack/Mattermost/Discord/Google Chat — is https; this rejects accidental http/typos.)
    - [ ] The override is **runtime-only**: it is held in memory and is **never persisted** to the database or disk.

### 2.3 Send a test alert

- **As the operator, I want to** send a test notification, **so that I can** confirm the webhook actually delivers before relying on it.
  - **Acceptance Criteria:**
    - [ ] A "send test alert" action POSTs a clearly-labelled test message (e.g. "✅ Test alert from wireguard-dashboard") to the **currently effective** webhook URL.
    - [ ] The UI reports the outcome: **delivered** (2xx) or **failed** (non-2xx / timeout / no URL configured), without exposing the URL in the result.
    - [ ] The test send uses the same delivery path (timeout + bounded retry, redacted logging) as real alerts.

### 2.4 Revert to the deploy-time value

- **As the operator, I want to** drop a runtime override, **so that I can** return to the SSM/env-provisioned webhook without a restart.
  - **Acceptance Criteria:**
    - [ ] A "revert" action discards the runtime override and restores the **deploy-time (env/SSM) value**, effective immediately.
    - [ ] If there was no deploy-time value, reverting returns the dashboard to the **disabled** state.
    - [ ] After any restart, the dashboard re-seeds from env/SSM regardless of prior UI changes (the override never survives a restart).

### 2.5 Security & access (the write-path exception)

- **As the operator, I want** webhook management to fit the dashboard's existing trust model, **so that** the security posture change is explicit and bounded.
  - **Acceptance Criteria:**
    - [ ] Webhook management adds **no authentication** — it inherits the dashboard's VPN-only access model. It is explicitly accepted that **any peer on the VPN can view the masked URL and set/redirect/revert the webhook**. (Trade-off recorded: a future-added peer could redirect alerts; acceptable for a solo-operator VPN, revisit if multi-user.)
    - [ ] The webhook URL is treated as a **secret**: never rendered in full in the UI, never logged in full (redacted to scheme+host), never persisted to the database or disk, never committed.
    - [ ] These are the dashboard's first **write/control** endpoints; they remain **outbound-effecting only** (they change where the dashboard POSTs) and add **no new inbound listener** and no change to the VPN-gated `http://172.16.15.1:8080` access path.
    - [ ] Alert delivery remains **opt-in**: with no env/SSM seed and no runtime override, the dashboard behaves exactly as today (alerts visible in-UI, nothing sent).

### 2.6 Placement & refresh

- **As the operator, I want** the controls to fit the existing layout, **so that** the dashboard stays coherent.
  - **Acceptance Criteria:**
    - [ ] Webhook management lives as a **card on the About tab** (the config/status surface), **no new tab** — keeping the Overview focused on live status.
    - [ ] The status display reflects the current state on the existing refresh model; a set/test/revert action updates the displayed status without a full page reload.
    - [ ] The controls are usable on mobile per 003 §2.10 (no horizontal page scroll ≥360px, touch targets ≥44px).

---

## 3. Scope and Boundaries

### In-Scope

- Viewing webhook status (enabled/disabled, masked current value, override-vs-deploy indicator).
- Setting/replacing the webhook URL at runtime (immediate effect, in-memory only).
- Sending a test alert to the current webhook and reporting the result.
- Reverting a runtime override back to the deploy-time (env/SSM) value.
- The deploy-time **seed**: wiring `DASHBOARD_WEBHOOK_URL` (and the 007 alert env knobs) into the host via SSM-read-by-Terraform → systemd `EnvironmentFile`, so the boot value exists to seed from. (This is the Terraform wiring deferred from 007; it belongs here as the boot-seed enabler.)

### Out-of-Scope

- **Persisting the override across restarts** — by design the env/SSM value re-seeds at boot; the UI override is ephemeral.
- **Authentication / authorization / per-user access** — the dashboard stays VPN-gated with no login; revisit only if it becomes multi-user.
- **Editing alert thresholds / windows via the UI** — 007's thresholds stay env-configured; UI tuning is a possible separate spec.
- **Multiple webhooks / multi-channel routing / non-webhook channels (email, bot tokens, PagerDuty)** — single Slack-compatible webhook only, unchanged from 007 §3.
- **Writing the override back to SSM** (no `ssm:PutParameter`) — the UI value is runtime-only; SSM is updated out-of-band as today.
- **Any other dashboard write/control operation** (peer add/remove/regen, service control) — still out of scope, unchanged from 002/003/007.
- **Changing the read-only/no-inbound-auth posture beyond these specific outbound-webhook controls.**
