// Package notify delivers alert messages to an outbound chat webhook. It is
// the dashboard's only egress path for spec 007 alerting and is deliberately
// transport-agnostic: the evaluator (poller dispatch) depends solely on the
// Notifier interface, so swapping the v1 Slack-compatible incoming-webhook
// transport for a future Slack-bot / Telegram / ntfy / PagerDuty backend is a
// drop-in with zero evaluator change. No transport specifics (Slack JSON shape,
// the webhook URL, retry tuning) leak past the interface.
//
// Implementations shipping here:
//
//   - Webhook — POSTs a {"text":…} JSON body over HTTPS to the URL resolved
//     per-send from a runtime-mutable WebhookConfig holder (spec 008), with a
//     per-request timeout and a bounded backoff retry on non-2xx / transport
//     errors. When the holder's current URL is empty the send is a no-op (no
//     HTTP attempt), which is the OFF state. This shape works against Slack-App
//     webhooks, Mattermost, Discord's …/slack endpoint, and Google Chat.
//   - SlackBot — POSTs {"channel","text"} to slack.com/api/chat.postMessage with
//     an Authorization: Bearer token (spec 012). Because Slack returns HTTP 200
//     even on failure with {"ok":false,"error":…}, it passes a response-body
//     validator to postJSON so an API-level rejection counts as a delivery
//     failure. Boot-config (env), a no-op when unconfigured.
//   - Telegram — POSTs {"chat_id","text"} to api.telegram.org/bot<token>/
//     sendMessage; the bot token lives in the URL path, so the log/error host
//     label is redacted to scheme+host. Boot-config (env), no-op when unset.
//   - Discord — POSTs {"content"} to a webhook URL (success is HTTP 204). The
//     webhook URL carries a secret token, so its host label is redacted. Boot-
//     config (env), no-op when unset.
//   - NoOp — a static OFF Notifier: Notify returns nil and makes no HTTP call.
//     The poller uses it as the nil-Notifier fallback so the dispatch path is
//     always non-nil. main.go itself always wires a holder-backed Webhook (whose
//     empty-URL state is the runtime-mutable OFF).
//   - MultiNotifier — a fan-out composite (spec 012) that delivers to every
//     child Notifier, isolating and aggregating failures, so the set of delivery
//     targets can grow while the evaluator/poller still depend on one Notifier.
//
// The shared JSON-POST + bounded-retry transport logic (postJSON) is factored
// out so the upcoming Slack-bot / Telegram / Discord transports reuse one
// implementation.
//
// The webhook URL is a secret and is NEVER logged in full: logs carry only a
// fixed "[webhook]" token or the bare host (see redactURL).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Notifier is the transport-agnostic seam every alert producer depends on.
// Notify delivers a single human-readable message and returns an error only
// when delivery definitively failed after the implementation's own retry
// budget is exhausted — callers (the evaluator, off the poll critical path)
// treat a returned error as "log and move on", never as fatal. Implementations
// MUST be safe for concurrent use and MUST honour ctx for cancellation/timeout.
type Notifier interface {
	Notify(ctx context.Context, message string) error
}

// HTTPDoer is the minimal slice of *http.Client the Webhook transport needs.
// Narrowing to this one method lets tests inject a fake that records the
// outgoing *http.Request and returns a canned response/error with no real
// network — *http.Client satisfies it directly in production.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Default transport tuning. All are overridable on the Webhook struct so tests
// can shrink the timeout/backoff to keep the suite fast and so a future operator
// knob (env-configurable) is a one-field change, not a code edit.
const (
	// defaultTimeout caps a single POST attempt. A chat webhook that hasn't
	// answered in 5s is treated as a failed attempt and retried; delivery runs
	// off the poll critical path so this never stalls a tick.
	defaultTimeout = 5 * time.Second

	// defaultMaxAttempts bounds the retry budget: the initial POST plus retries,
	// total. Capped so a persistently-down receiver can't spin forever — after
	// the budget is spent Notify gives up and returns an error.
	defaultMaxAttempts = 3

	// defaultBackoff is the base inter-attempt pause; it grows linearly with the
	// attempt index (1×, 2×, …) for a simple bounded backoff without jitter —
	// good enough for a single best-effort alert delivery.
	defaultBackoff = 500 * time.Millisecond
)

// Webhook is the v1 Slack-compatible Notifier. It resolves its target URL from
// a runtime-mutable WebhookConfig holder on EVERY send (spec 008), so an
// operator can re-point or disable delivery without a restart — there is no
// cached URL on the struct. When the holder's current URL is empty Notify is a
// no-op (returns nil, NO HTTP attempt). Otherwise it POSTs {"text":message} as
// JSON over HTTPS, retrying on non-2xx responses and transport errors up to
// MaxAttempts with a linear backoff between tries. Construct with NewNotifier;
// exported fields exist so same-package tests can build a literal with a fake
// Client and shrunken timing. Safe for concurrent use (the holder is mutex-
// guarded, the underlying *http.Client is, and Notify holds no mutable state).
type Webhook struct {
	// Config resolves the secret incoming-webhook endpoint per send. Never nil
	// in a struct built by NewNotifier. The URL it returns is never logged in
	// full — redactURL reduces it to scheme+host for any log/error.
	Config *WebhookConfig
	// Client is the HTTP transport seam. nil is treated as a default
	// *http.Client with Timeout=Timeout at send time.
	Client HTTPDoer
	// Timeout bounds a single attempt. Zero means defaultTimeout.
	Timeout time.Duration
	// MaxAttempts bounds total tries (initial + retries). Zero/negative means
	// defaultMaxAttempts.
	MaxAttempts int
	// Backoff is the base linear backoff between attempts. Zero means
	// defaultBackoff.
	Backoff time.Duration
}

// NewNotifier constructs a holder-backed Webhook with production defaults: a
// 5s-timeout *http.Client, up to 3 attempts, 500ms linear backoff. cfg must be
// non-nil; it is the single source of truth for the (possibly empty) target URL
// and is consulted on every send. An empty current URL is the OFF state — no
// error, no HTTP — so unlike the old URL-present branch, main.go always wires
// this one notifier regardless of whether a URL is configured at boot.
func NewNotifier(cfg *WebhookConfig) *Webhook {
	return &Webhook{
		Config:      cfg,
		Client:      &http.Client{Timeout: defaultTimeout},
		Timeout:     defaultTimeout,
		MaxAttempts: defaultMaxAttempts,
		Backoff:     defaultBackoff,
	}
}

// slackPayload is the Slack-compatible incoming-webhook body. Kept unexported
// so the JSON shape never escapes the package — callers only ever see the
// Notifier interface and pass a plain string.
type slackPayload struct {
	Text string `json:"text"`
}

// Notify resolves the current target URL from the holder and, if non-empty,
// POSTs message as {"text":message} JSON to it, retrying on non-2xx / transport
// failure up to MaxAttempts with a linear backoff, then giving up with an error.
// If the current URL is empty it returns nil immediately with NO HTTP attempt
// (the runtime-mutable OFF state). It never panics on transport failure and
// never logs the full URL — only the redacted host. ctx cancellation aborts
// immediately between or during attempts.
func (w *Webhook) Notify(ctx context.Context, message string) error {
	target := w.Config.Current()
	if target == "" {
		// Runtime OFF: no URL configured. Match the old NoOp semantics exactly —
		// no network, no logging, no error.
		return nil
	}

	// Delegate to the shared JSON-POST + bounded-retry helper. The Slack-
	// compatible {"text":…} body and the per-send-resolved (mutable) URL are the
	// only webhook-specific bits; timeout/attempts/backoff defaults are applied by
	// the helper when these fields are zero, matching the prior behaviour exactly.
	return postJSON(ctx, w.Client, target, nil, slackPayload{Text: message}, httpRetry{
		Timeout:     w.Timeout,
		MaxAttempts: w.MaxAttempts,
		Backoff:     w.Backoff,
	}, redactURL(target), nil)
}

// redactURL reduces a webhook URL to a non-secret label for logging. It returns
// the scheme+host (e.g. "https://hooks.slack.com") which is safe to surface —
// the secret path/token component is dropped. If the URL can't be parsed or has
// no host, it falls back to the fixed "[webhook]" token so a log line never
// carries the raw secret even on a parse failure.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[webhook]"
	}
	if u.Scheme == "" {
		return u.Host
	}
	return u.Scheme + "://" + u.Host
}

// NoOp is a static OFF Notifier: Notify does nothing, returns nil, and makes no
// HTTP call. The poller uses it as the nil-Notifier fallback on the dispatch
// path so delivery is always callable. (main.go wires a holder-backed Webhook,
// whose empty-URL state is the runtime-mutable OFF.)
type NoOp struct{}

// Notify discards the message and returns nil. No network, no logging.
func (NoOp) Notify(ctx context.Context, message string) error { return nil }

// MultiNotifier fans a single alert out to every child Notifier (spec 012). It
// is the composite main.go wraps the configured transports in, so the evaluator/
// poller/dispatch keep depending on one Notifier while delivery targets grow.
// Failures are ISOLATED — a child returning an error never short-circuits the
// remaining children — and AGGREGATED via errors.Join so the caller sees every
// failure at once. An empty (or all-nil) composite is a safe no-op. Safe for
// concurrent use: it holds no mutable state and children must be too.
type MultiNotifier struct {
	children []Notifier
}

// NewMultiNotifier builds a composite from children, dropping any nil entries so
// callers can pass optionally-constructed transports without a nil-panic on
// dispatch. The result is always non-nil and safe to call even when empty.
func NewMultiNotifier(children ...Notifier) Notifier {
	filtered := make([]Notifier, 0, len(children))
	for _, c := range children {
		if c != nil {
			filtered = append(filtered, c)
		}
	}
	return &MultiNotifier{children: filtered}
}

// Notify delivers message to every child in order, isolating failures: a child
// returning an error is recorded but never stops the remaining children from
// being called. All child errors are combined with errors.Join and returned
// together; the result is nil when every child succeeds or the list is empty.
func (m *MultiNotifier) Notify(ctx context.Context, message string) error {
	var errs []error
	for _, c := range m.children {
		if err := c.Notify(ctx, message); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// compile-time assertions that every implementation satisfies Notifier.
var (
	_ Notifier = (*Webhook)(nil)
	_ Notifier = (*SlackBot)(nil)
	_ Notifier = (*Telegram)(nil)
	_ Notifier = (*Discord)(nil)
	_ Notifier = NoOp{}
	_ Notifier = (*MultiNotifier)(nil)
)

// --- Boot-config transports (spec 012, Slice 3) ----------------------------
//
// Each of the three transports below is an immutable, boot-config Notifier (no
// runtime holder, unlike the v1 Webhook): its config is read once from the
// environment by the matching New*FromEnv constructor and never changes for the
// process lifetime. Every transport is a no-op when its env is unset, reuses the
// shared postJSON helper (per-attempt timeout + bounded retry), and redacts any
// secret-bearing URL/token out of logs and returned errors.
//
// NOTE on the nil-interface gotcha: the New*FromEnv constructors return the
// Notifier interface and return an untyped nil (not a typed (*SlackBot)(nil))
// when unconfigured, so NewMultiNotifier's `c != nil` filter actually drops
// them. Returning a typed nil pointer would slip past that check and panic on
// dispatch.

// slackPostMessageURL is the Slack Web API chat.postMessage endpoint. The URL
// itself is not secret (the bot token rides in the Authorization header), so it
// is also the log/error host label.
const slackPostMessageURL = "https://slack.com/api/chat.postMessage"

// SlackBot delivers via the Slack Web API's chat.postMessage method using a bot
// token (xoxb-…) in an Authorization: Bearer header — distinct from the v1
// incoming-webhook Webhook. The token NEVER appears in a URL or a log line: it
// lives only in the request header, and the host label is the fixed, non-secret
// "slack.com". Slack answers HTTP 200 even on failure with {"ok":false,
// "error":…}, so Notify hands postJSON a body validator that turns ok:false into
// a delivery error surfacing Slack's own error string. Safe for concurrent use
// (immutable after construction; the *http.Client is). A zero token or channel
// makes Notify a no-op.
type SlackBot struct {
	// Token is the Slack bot token (xoxb-…). Secret — header only, never logged.
	Token string
	// Channel is the target channel id or name (e.g. "#alerts" or "C0123").
	Channel string
	// Client is the HTTP transport seam; nil means a default *http.Client with
	// Timeout=Timeout at send time.
	Client HTTPDoer
	// Timeout / MaxAttempts / Backoff tune the shared retry budget; zero values
	// fall back to the package defaults (5s / 3 / 500ms).
	Timeout     time.Duration
	MaxAttempts int
	Backoff     time.Duration
}

// slackBotPayload is the chat.postMessage request body. Unexported so the JSON
// shape never escapes the package.
type slackBotPayload struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// slackAPIResponse is the slice of chat.postMessage's JSON ack we inspect. Slack
// returns ok:true on success and ok:false with a machine-readable error code
// (e.g. "channel_not_found", "invalid_auth") on failure — both under HTTP 200.
type slackAPIResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// NewSlackBotFromEnv builds a SlackBot from DASHBOARD_SLACK_BOT_TOKEN +
// DASHBOARD_SLACK_CHANNEL. It is enabled only when BOTH are set; otherwise it
// returns a nil Notifier (untyped — so NewMultiNotifier filters it out). The
// returned notifier carries production defaults (5s timeout, 3 attempts, 500ms
// backoff) and a default *http.Client.
func NewSlackBotFromEnv() Notifier {
	token := os.Getenv("DASHBOARD_SLACK_BOT_TOKEN")
	channel := os.Getenv("DASHBOARD_SLACK_CHANNEL")
	if token == "" || channel == "" {
		return nil
	}
	return &SlackBot{
		Token:       token,
		Channel:     channel,
		Client:      &http.Client{Timeout: defaultTimeout},
		Timeout:     defaultTimeout,
		MaxAttempts: defaultMaxAttempts,
		Backoff:     defaultBackoff,
	}
}

// Notify POSTs {channel,text} to chat.postMessage with the bot token in the
// Authorization header, retrying transport errors, non-2xx status, AND Slack
// ok:false bodies up to MaxAttempts. A zero token/channel is a no-op (nil, no
// HTTP). The token is never logged — only the fixed "slack.com" host label is.
func (s *SlackBot) Notify(ctx context.Context, message string) error {
	if s == nil || s.Token == "" || s.Channel == "" {
		return nil
	}
	headers := map[string]string{"Authorization": "Bearer " + s.Token}
	return postJSON(ctx, s.Client, slackPostMessageURL, headers,
		slackBotPayload{Channel: s.Channel, Text: message},
		httpRetry{Timeout: s.Timeout, MaxAttempts: s.MaxAttempts, Backoff: s.Backoff},
		"slack.com", validateSlackResponse)
}

// validateSlackResponse turns a chat.postMessage 200 body into a delivery error
// when ok is false, surfacing Slack's own (non-secret) error code. An
// unparseable body is also a failure — a 200 that isn't the expected JSON ack
// means we can't confirm delivery. The bot token is not present in this body.
func validateSlackResponse(body []byte) error {
	var r slackAPIResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("notify: slack.com returned an unparseable response")
	}
	if !r.OK {
		return fmt.Errorf("notify: slack.com rejected message: %s", r.Error)
	}
	return nil
}

// Telegram delivers via the Bot API sendMessage method. The bot token lives in
// the URL PATH (…/bot<token>/sendMessage), so unlike SlackBot the secret is in
// the URL — Notify redacts the host to scheme+host ("https://api.telegram.org")
// for every log line and error so the token never leaks. A zero token or chat id
// makes Notify a no-op. Safe for concurrent use (immutable after construction).
type Telegram struct {
	// Token is the bot token (…:…). Secret — rides in the URL path; redacted out
	// of logs.
	Token string
	// ChatID is the target chat id (numeric, or "@channelname"). Sent as a JSON
	// string, which the Bot API accepts.
	ChatID string
	// Client / Timeout / MaxAttempts / Backoff: see SlackBot.
	Client      HTTPDoer
	Timeout     time.Duration
	MaxAttempts int
	Backoff     time.Duration
}

// telegramPayload is the sendMessage request body.
type telegramPayload struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
}

// NewTelegramFromEnv builds a Telegram transport from DASHBOARD_TELEGRAM_TOKEN +
// DASHBOARD_TELEGRAM_CHAT_ID. Enabled only when BOTH are set; otherwise a nil
// Notifier. Production defaults + default *http.Client, as the others.
func NewTelegramFromEnv() Notifier {
	token := os.Getenv("DASHBOARD_TELEGRAM_TOKEN")
	chatID := os.Getenv("DASHBOARD_TELEGRAM_CHAT_ID")
	if token == "" || chatID == "" {
		return nil
	}
	return &Telegram{
		Token:       token,
		ChatID:      chatID,
		Client:      &http.Client{Timeout: defaultTimeout},
		Timeout:     defaultTimeout,
		MaxAttempts: defaultMaxAttempts,
		Backoff:     defaultBackoff,
	}
}

// Notify POSTs {chat_id,text} to sendMessage. A zero token/chat id is a no-op
// (nil, no HTTP). The token is in the URL path, so the host label passed to
// postJSON is redacted to scheme+host and the full URL is never logged.
func (t *Telegram) Notify(ctx context.Context, message string) error {
	if t == nil || t.Token == "" || t.ChatID == "" {
		return nil
	}
	endpoint := "https://api.telegram.org/bot" + t.Token + "/sendMessage"
	return postJSON(ctx, t.Client, endpoint, nil,
		telegramPayload{ChatID: t.ChatID, Text: message},
		httpRetry{Timeout: t.Timeout, MaxAttempts: t.MaxAttempts, Backoff: t.Backoff},
		redactURL(endpoint), nil)
}

// Discord delivers via a webhook URL whose path carries a secret token, so the
// host label is redacted (scheme+host). Discord answers HTTP 204 No Content on
// success, which the shared 2xx check already accepts. An empty webhook URL makes
// Notify a no-op. Safe for concurrent use (immutable after construction).
type Discord struct {
	// WebhookURL is the full Discord webhook endpoint. Secret (path token) —
	// redacted out of logs.
	WebhookURL string
	// Client / Timeout / MaxAttempts / Backoff: see SlackBot.
	Client      HTTPDoer
	Timeout     time.Duration
	MaxAttempts int
	Backoff     time.Duration
}

// discordPayload is the webhook request body.
type discordPayload struct {
	Content string `json:"content"`
}

// NewDiscordFromEnv builds a Discord transport from DASHBOARD_DISCORD_WEBHOOK_URL.
// Enabled when set; otherwise a nil Notifier. Production defaults + default
// *http.Client.
func NewDiscordFromEnv() Notifier {
	webhookURL := os.Getenv("DASHBOARD_DISCORD_WEBHOOK_URL")
	if webhookURL == "" {
		return nil
	}
	return &Discord{
		WebhookURL:  webhookURL,
		Client:      &http.Client{Timeout: defaultTimeout},
		Timeout:     defaultTimeout,
		MaxAttempts: defaultMaxAttempts,
		Backoff:     defaultBackoff,
	}
}

// Notify POSTs {content} to the webhook URL. An empty URL is a no-op (nil, no
// HTTP). Success is HTTP 204. The URL is secret, so the host label is redacted.
func (d *Discord) Notify(ctx context.Context, message string) error {
	if d == nil || d.WebhookURL == "" {
		return nil
	}
	return postJSON(ctx, d.Client, d.WebhookURL, nil,
		discordPayload{Content: message},
		httpRetry{Timeout: d.Timeout, MaxAttempts: d.MaxAttempts, Backoff: d.Backoff},
		redactURL(d.WebhookURL), nil)
}

// httpRetry bundles the per-attempt timeout and bounded linear-backoff retry
// budget shared by every JSON-POST transport (the v1 Webhook today; the Slack-
// bot / Telegram / Discord transports next slice). A zero field falls back to
// the package defaults (5s / 3 attempts / 500ms) so a transport that doesn't
// tune them gets the same behaviour the Webhook has always had.
type httpRetry struct {
	Timeout     time.Duration
	MaxAttempts int
	Backoff     time.Duration
}

func (r httpRetry) timeout() time.Duration {
	if r.Timeout > 0 {
		return r.Timeout
	}
	return defaultTimeout
}

func (r httpRetry) maxAttempts() int {
	if r.MaxAttempts > 0 {
		return r.MaxAttempts
	}
	return defaultMaxAttempts
}

func (r httpRetry) backoff() time.Duration {
	if r.Backoff > 0 {
		return r.Backoff
	}
	return defaultBackoff
}

// postJSON marshals payload to JSON and POSTs it to url, retrying on transport
// errors and non-2xx status with a bounded linear backoff up to retry.maxAttempts.
// Content-Type: application/json is always set; extra headers (e.g. an
// Authorization bearer for the Slack-bot transport) are layered on top. host is
// the caller-redacted label surfaced in every log line and returned error, so a
// secret url/token never leaks even on a build/transport failure. A nil client
// is treated as a default *http.Client with Timeout=retry.timeout(). ctx
// cancellation aborts promptly, both during the backoff wait and within an
// attempt. Returns nil on the first 2xx, else an error after the budget is spent.
//
// validate is an optional response-body hook for APIs that signal failure in the
// body rather than the status line — Slack's chat.postMessage returns HTTP 200
// with {"ok":false,"error":…}, so a status check alone is insufficient. When
// validate is non-nil it runs on a successful (2xx) response's body and a
// non-nil result is treated as a failed attempt (subject to the same retry
// budget). It MUST NOT surface secrets in its error — the body is third-party,
// but the validator's message is the caller's responsibility. Pass nil for
// transports whose 2xx status is authoritative (the v1 webhook, Telegram,
// Discord).
func postJSON(ctx context.Context, client HTTPDoer, url string, headers map[string]string, payload any, retry httpRetry, host string, validate func([]byte) error) error {
	body, err := json.Marshal(payload)
	if err != nil {
		// json.Marshal of these small fixed-shape payloads cannot fail in
		// practice; guard anyway so a future payload change degrades loudly.
		return fmt.Errorf("notify: marshal payload: %w", err)
	}

	if client == nil {
		client = &http.Client{Timeout: retry.timeout()}
	}

	maxAttempts := retry.maxAttempts()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Bounded linear backoff; abort the wait if ctx is cancelled so we
			// don't sleep past a shutdown.
			select {
			case <-ctx.Done():
				return fmt.Errorf("notify: aborted before attempt %d: %w", attempt, ctx.Err())
			case <-time.After(retry.backoff() * time.Duration(attempt-1)):
			}
		}

		lastErr = postOnce(ctx, client, url, headers, body, retry.timeout(), host, validate)
		if lastErr == nil {
			return nil
		}
		slog.Warn("notify: delivery attempt failed",
			"host", host, "attempt", attempt, "max", maxAttempts, "err", lastErr)
	}

	slog.Error("notify: delivery gave up", "host", host, "attempts", maxAttempts, "err", lastErr)
	return fmt.Errorf("notify: delivery failed after %d attempts: %w", maxAttempts, lastErr)
}

// postOnce performs a single POST attempt. It applies a per-attempt timeout
// derived from ctx and returns a non-nil error on any transport failure or
// non-2xx status. host is the precomputed redaction used in all error strings so
// the secret never appears in a returned error. When validate is non-nil and the
// response is 2xx, the body is read (bounded) and passed to validate so an
// API-level failure carried in a 200 body (Slack ok:false) still counts as a
// failed attempt.
func postOnce(ctx context.Context, client HTTPDoer, url string, headers map[string]string, body []byte, timeout time.Duration, host string, validate func([]byte) error) error {
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		// http.NewRequestWithContext can surface the URL in its error; redact.
		return fmt.Errorf("notify: build request to %s failed", host)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		// A transport error (incl. timeout) may embed the URL; redact it.
		return fmt.Errorf("notify: POST to %s failed", host)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("notify: %s returned status %d", host, resp.StatusCode)
	}

	if validate != nil {
		// Bound the read: a misbehaving endpoint must not let us slurp an
		// unbounded body into memory. 64 KiB is far more than any of these
		// JSON ack bodies need.
		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if err != nil {
			return fmt.Errorf("notify: reading response from %s failed", host)
		}
		return validate(respBody)
	}
	return nil
}
