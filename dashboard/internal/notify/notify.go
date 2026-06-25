// Package notify delivers alert messages to an outbound chat webhook. It is
// the dashboard's only egress path for spec 007 alerting and is deliberately
// transport-agnostic: the evaluator (poller dispatch) depends solely on the
// Notifier interface, so swapping the v1 Slack-compatible incoming-webhook
// transport for a future Slack-bot / Telegram / ntfy / PagerDuty backend is a
// drop-in with zero evaluator change. No transport specifics (Slack JSON shape,
// the webhook URL, retry tuning) leak past the interface.
//
// Two implementations ship here:
//
//   - Webhook — POSTs a {"text":…} JSON body over HTTPS to the URL resolved
//     per-send from a runtime-mutable WebhookConfig holder (spec 008), with a
//     per-request timeout and a bounded backoff retry on non-2xx / transport
//     errors. When the holder's current URL is empty the send is a no-op (no
//     HTTP attempt), which is the OFF state. This shape works against Slack-App
//     webhooks, Mattermost, Discord's …/slack endpoint, and Google Chat.
//   - NoOp — a static OFF Notifier: Notify returns nil and makes no HTTP call.
//     The poller uses it as the nil-Notifier fallback so the dispatch path is
//     always non-nil. main.go itself always wires a holder-backed Webhook (whose
//     empty-URL state is the runtime-mutable OFF).
//
// The webhook URL is a secret and is NEVER logged in full: logs carry only a
// fixed "[webhook]" token or the bare host (see redactURL).
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
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
	host := redactURL(target)

	body, err := json.Marshal(slackPayload{Text: message})
	if err != nil {
		// json.Marshal of a struct with a single string field cannot fail in
		// practice; guard anyway so a future payload change degrades loudly.
		return fmt.Errorf("notify: marshal payload: %w", err)
	}

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: w.timeout()}
	}

	maxAttempts := w.maxAttempts()
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			// Bounded linear backoff; abort the wait if ctx is cancelled so we
			// don't sleep past a shutdown.
			select {
			case <-ctx.Done():
				return fmt.Errorf("notify: aborted before attempt %d: %w", attempt, ctx.Err())
			case <-time.After(w.backoff() * time.Duration(attempt-1)):
			}
		}

		lastErr = w.post(ctx, client, target, host, body)
		if lastErr == nil {
			return nil
		}
		slog.Warn("notify: webhook delivery attempt failed",
			"host", host, "attempt", attempt, "max", maxAttempts, "err", lastErr)
	}

	slog.Error("notify: webhook delivery gave up", "host", host, "attempts", maxAttempts, "err", lastErr)
	return fmt.Errorf("notify: webhook delivery failed after %d attempts: %w", maxAttempts, lastErr)
}

// post performs one POST attempt to target. It applies a per-attempt timeout
// derived from ctx and returns a non-nil error on any transport failure or
// non-2xx status. host is the precomputed redaction of target used in all
// error strings so the secret never appears in a returned error.
func (w *Webhook) post(ctx context.Context, client HTTPDoer, target, host string, body []byte) error {
	attemptCtx, cancel := context.WithTimeout(ctx, w.timeout())
	defer cancel()

	req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		// http.NewRequestWithContext can surface the URL in its error; redact.
		return fmt.Errorf("notify: build request to %s failed", host)
	}
	req.Header.Set("Content-Type", "application/json")

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
	return nil
}

func (w *Webhook) timeout() time.Duration {
	if w.Timeout > 0 {
		return w.Timeout
	}
	return defaultTimeout
}

func (w *Webhook) maxAttempts() int {
	if w.MaxAttempts > 0 {
		return w.MaxAttempts
	}
	return defaultMaxAttempts
}

func (w *Webhook) backoff() time.Duration {
	if w.Backoff > 0 {
		return w.Backoff
	}
	return defaultBackoff
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

// compile-time assertions that both implementations satisfy Notifier.
var (
	_ Notifier = (*Webhook)(nil)
	_ Notifier = NoOp{}
)
