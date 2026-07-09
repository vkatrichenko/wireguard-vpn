# Alerting & Notifications Domain

## TL;DR

- Pure in-memory alert evaluator drives edge-triggered per-condition state machines once per poller sample tick.
- Four conditions: service-down, high-disk, high-cpu, transfer-cap (transfer-cap is per-client sub-keyed).
- Re-notification while FIRING is suppressed until the 30-minute DefaultCooldown elapses.
- Delivery is transport-agnostic via the Notifier interface: Webhook, SlackBot, Telegram, Discord, NoOp, MultiNotifier.
- Key files: `dashboard/internal/alerts/alerts.go`, `dashboard/internal/alerts/status.go`, `dashboard/internal/notify/notify.go`, `dashboard/internal/notify/webhookconfig.go`, `dashboard/internal/server/handlers_alerts.go`, `dashboard/internal/server/handlers_webhook.go`.

This route governs how the dashboard detects, tracks, and delivers operator-facing alerts for host and tunnel conditions.

## Purpose

This route owns condition evaluation (is something wrong right now?) and outbound notification delivery (tell the operator). It exists so the solo operator learns about service outages, disk pressure, sustained CPU load, or a peer blowing through a transfer cap without polling the dashboard UI. Evaluation and delivery are deliberately split into two independently testable layers: a pure state machine with no I/O, and a set of swappable HTTP-based transports.

## Core Concepts

- Evaluator (`internal/alerts`) is the pure, in-memory alert core with zero I/O — no exec, no /proc, no network, no DB.
- The poller drives `Evaluator.Evaluate` exactly once per sample tick and dispatches the returned Events off the poll critical path.
- Condition identifies a watched class: `ConditionServiceDown` ("service-down"), `ConditionHighDisk` ("high-disk"), `ConditionHighCPU` ("high-cpu"), `ConditionTransferCap` ("transfer-cap").
- ConditionTransferCap is the only per-client condition — its Key suffixes the client name so one noisy peer cannot suppress another peer's alerts.
- Kind is Fire (OK to FIRING transition, or a post-cooldown reminder) or Recovery (FIRING to OK transition).
- Each condition Key runs its own OK/FIRING state machine.
- State lives only in the Evaluator's in-memory map.
- DefaultCooldown is 30 minutes — the minimum gap between successive Fire events for a single still-firing condition.
- DefaultDiskThresholdPct is 90.0 percent filesystem fullness.
- DefaultCPUThresholdPct is 90.0 percent sustained for DefaultCPUSustain (5 minutes).
- DefaultTransferCapBytes is 50 GiB per client, per direction.
- StatusHolder (`internal/alerts/status.go`) is the ONLY safe channel between the poller goroutine and HTTP handler goroutines.
- Notifier (`internal/notify`) is the transport-agnostic delivery interface every alert producer depends on: `Notify(ctx, message) error`.
- Webhook posts a generic `{"text":...}` JSON body, compatible with Slack-App incoming webhooks, Mattermost, Discord's `/slack` endpoint, and Google Chat.
- Webhook resolves its target URL per-send from a runtime-mutable WebhookConfig holder rather than caching it — an operator can re-point delivery without a restart.
- SlackBot delivers via `chat.postMessage` with a bearer token and validates the response body, because Slack returns HTTP 200 even when `{"ok":false}`.
- Telegram carries its bot token in the URL path, redacted to host-only before ever reaching a log line.
- Discord carries a secret token in its webhook URL, redacted to host-only before ever reaching a log line.
- Discord's success status is HTTP 204, distinct from the other transports' 2xx-body-based checks.
- NoOp is the static OFF fallback Notifier — Notify is a no-op returning nil.
- MultiNotifier fans one alert out to every configured transport, isolating and aggregating child failures so one broken transport never blocks the others.
- WebhookConfig (`internal/notify/webhookconfig.go`) holds an immutable boot `seed` (from `DASHBOARD_WEBHOOK_URL`) plus an optional runtime `override`.
- A nil WebhookConfig override means "use the seed".
- The WebhookConfig override is never persisted, so a process restart reverts delivery to the seed value.
- `GET /api/alerts` (`handlers_alerts.go`) serves current in-UI alert state as JSON: `{enabled, active[], recent[]}`.
- `GET /api/webhook`, `POST /api/webhook`, `POST /api/webhook/revert`, `POST /api/webhook/test` (`handlers_webhook.go`) are the spec 008 sanctioned runtime webhook-management write endpoints.
- These webhook endpoints are already referenced as sanctioned write paths by the Web Delivery & UI route; this route owns their behavioral rules.
- Spec 007 defines the alert evaluator core and delivery.
- Spec 008 adds runtime webhook config endpoints.
- Spec 012 adds SlackBot/Telegram/Discord multi-transport plus MultiNotifier.

## Invariants

These rules must never be violated:
- The Evaluator performs zero I/O — no exec, no /proc, no network, no DB access inside `alerts.Evaluate`.
- The Evaluator is NOT safe for concurrent use — it is touched only from the single poller goroutine that drives the sample tick.
- HTTP handlers must never call the Evaluator directly.
- HTTP handlers read alert state only through the StatusHolder's deep-copied Snapshot.
- Alert state has no persistence — it lives only in the Evaluator's in-memory map and is lost on process restart.
- A process restart re-arms every condition Key from OK.
- A condition that is still bad on the first post-restart tick fires once more — an accepted tradeoff, not a bug.
- Exactly one Fire event is emitted on the OK to FIRING transition.
- No Fire events are emitted while a condition is FIRING and inside its cooldown window.
- Exactly one Recovery event is emitted on the FIRING to OK transition.
- A still-bad condition emits a reminder Fire once DefaultCooldown (30 minutes) elapses, restarting the cooldown clock.
- A Recovery event re-arms the condition Key so the next OK to FIRING transition fires immediately again.
- The transfer-cap condition is per-client sub-keyed (`transfer-cap:<name>`) so one noisy peer's alert state never suppresses another client's alerts.
- Transfer-cap counters only increase, so a fired transfer-cap alert never naturally recovers from traffic alone.
- The only transfer-cap recovery path is a wg counter reset or wrap, which re-baselines and re-arms the state machine.
- Webhook URLs, bot tokens, and Discord webhook secrets are never logged in full.
- Only a redacted host or a fixed `[webhook]` token appears in logs and error strings for webhook/bot secrets.
- A WebhookConfig runtime override is never persisted to disk or the DB.
- A dashboard restart always reverts delivery to the boot-seeded `DASHBOARD_WEBHOOK_URL`.
- `GET /api/alerts` never returns 500 for a nil StatusHolder (alerting wiring absent, e.g. tests) — it returns a valid empty response instead.
- `POST /api/webhook` rejects any non-https:// URL or a URL with an empty host with 400, leaving the holder unchanged.
- The boot-config transports (SlackBot, Telegram, Discord) are disabled (nil Notifier) unless their required env vars are all set.
- `NewMultiNotifier` filters nil transport entries so an unconfigured transport never panics on dispatch.

## Route-Specific Constraints

- Adding a new condition means computing its (firingNow, detail) booleans and calling the shared `observe` funnel — never re-implementing OK/FIRING transition logic per condition.
- New Notifier implementations must satisfy the `Notify(ctx, message) error` interface and must not leak secrets (URLs, tokens) into logs or returned errors.
- New boot-config transports must return an untyped nil Notifier when unconfigured, not a typed nil pointer, or `MultiNotifier`'s `c != nil` filter will not catch it and dispatch will panic.
- The CPU sustain window and per-condition thresholds are Evaluator `Config` fields with `Default*` const fallbacks — do not hardcode thresholds elsewhere.
- The webhook management HTTP endpoints content-negotiate: htmx requests (`HX-Request: true`) get a re-rendered HTML card fragment, non-htmx requests keep the plain JSON contract.
- `POST /api/webhook/test` must never echo the configured webhook URL in its response body, even on delivery failure — failure reasons are fixed, generic strings.
