package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testSeedURL = "https://hooks.example.com/services/SECRET/TOKEN/abc123"

// fakeDoer is an injectable HTTPDoer: it records every request and returns a
// canned response/error chosen by the test. calls counts attempts so a test can
// assert the retry budget (or that an empty-URL send never reaches a transport).
type fakeDoer struct {
	calls    atomic.Int32
	requests []*http.Request
	bodies   []string
	resp     func(attempt int) (*http.Response, error)
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	n := int(f.calls.Add(1))
	f.requests = append(f.requests, req)
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		f.bodies = append(f.bodies, string(b))
	}
	return f.resp(n)
}

func okResp() *http.Response {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}
}

func statusResp(code int) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(""))}
}

// newTestWebhook builds a holder-backed Webhook seeded with seedURL, shrunken
// timing so retry tests don't burn real wall-clock, and a fake transport so no
// real network is touched. It returns the notifier and its holder so a test can
// mutate the URL mid-flight.
func newTestWebhook(seedURL string, doer HTTPDoer) (*Webhook, *WebhookConfig) {
	cfg := NewWebhookConfig(seedURL)
	return &Webhook{
		Config:      cfg,
		Client:      doer,
		Timeout:     50 * time.Millisecond,
		MaxAttempts: 3,
		Backoff:     time.Millisecond,
	}, cfg
}

// (a) correct payload shape {"text":...} + Content-Type, POSTed to the seed URL.
func TestNotify_PayloadShapeAndHeaders(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return okResp(), nil }}
	w, _ := newTestWebhook(testSeedURL, doer)

	if err := w.Notify(context.Background(), "hello world"); err != nil {
		t.Fatalf("Notify: unexpected error: %v", err)
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("want exactly 1 HTTP attempt, got %d", doer.calls.Load())
	}

	req := doer.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.String() != testSeedURL {
		t.Errorf("posted to %q, want seed %q", req.URL.String(), testSeedURL)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(doer.bodies[0]), &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v (body=%q)", err, doer.bodies[0])
	}
	if len(payload) != 1 {
		t.Errorf("payload has %d keys, want exactly 1 (text)", len(payload))
	}
	if payload["text"] != "hello world" {
		t.Errorf("payload[text] = %v, want %q", payload["text"], "hello world")
	}
}

// Empty current URL → true no-op: returns nil and never touches the transport.
func TestNotify_EmptyURLNoOp(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) {
		t.Fatal("transport must not be called when current URL is empty")
		return nil, nil
	}}
	w, _ := newTestWebhook("", doer)

	if err := w.Notify(context.Background(), "dropped"); err != nil {
		t.Fatalf("empty-URL Notify returned %v, want nil", err)
	}
	if doer.calls.Load() != 0 {
		t.Fatalf("want 0 HTTP attempts on empty URL, got %d", doer.calls.Load())
	}
}

// Set re-points delivery at runtime; Revert restores the seed — both resolved
// per send with no restart.
func TestNotify_ResolvesURLPerSend(t *testing.T) {
	const otherURL = "https://other.example.net/hook/XYZ"
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return okResp(), nil }}
	w, cfg := newTestWebhook(testSeedURL, doer)

	mustPostTo := func(want string) {
		t.Helper()
		before := len(doer.requests)
		if err := w.Notify(context.Background(), "m"); err != nil {
			t.Fatalf("Notify: %v", err)
		}
		got := doer.requests[before].URL.String()
		if got != want {
			t.Fatalf("posted to %q, want %q", got, want)
		}
	}

	mustPostTo(testSeedURL) // seed
	cfg.Set(otherURL)
	mustPostTo(otherURL) // override
	cfg.Revert()
	mustPostTo(testSeedURL) // back to seed
}

// Setting an empty override at runtime disables delivery (no HTTP).
func TestNotify_OverrideToEmptyDisables(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return okResp(), nil }}
	w, cfg := newTestWebhook(testSeedURL, doer)
	cfg.Set("")

	if err := w.Notify(context.Background(), "m"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if doer.calls.Load() != 0 {
		t.Fatalf("empty override should suppress HTTP, got %d attempts", doer.calls.Load())
	}
}

// (b) 2xx → success, no retry.
func TestNotify_Success2xx(t *testing.T) {
	for _, code := range []int{200, 201, 204, 299} {
		doer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(code), nil }}
		w, _ := newTestWebhook(testSeedURL, doer)
		if err := w.Notify(context.Background(), "m"); err != nil {
			t.Errorf("status %d: unexpected error %v", code, err)
		}
		if doer.calls.Load() != 1 {
			t.Errorf("status %d: want 1 attempt, got %d", code, doer.calls.Load())
		}
	}
}

// (c1) non-2xx → bounded retry then give up gracefully (error returned, no panic).
func TestNotify_Non2xxBoundedRetry(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(500), nil }}
	w, _ := newTestWebhook(testSeedURL, doer)

	err := w.Notify(context.Background(), "m")
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	if got := doer.calls.Load(); got != 3 {
		t.Fatalf("want exactly MaxAttempts=3 attempts, got %d", got)
	}
}

// (c2) transport timeout/error → bounded retry then give up gracefully.
func TestNotify_TransportErrorBoundedRetry(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) {
		return nil, errors.New("simulated dial timeout")
	}}
	w, _ := newTestWebhook(testSeedURL, doer)

	err := w.Notify(context.Background(), "m")
	if err == nil {
		t.Fatal("want error after exhausting retries, got nil")
	}
	if got := doer.calls.Load(); got != 3 {
		t.Fatalf("want exactly 3 attempts, got %d", got)
	}
}

// (c3) recovers if a later attempt succeeds — proves retry, not just give-up.
func TestNotify_SucceedsOnRetry(t *testing.T) {
	doer := &fakeDoer{resp: func(attempt int) (*http.Response, error) {
		if attempt < 2 {
			return statusResp(503), nil
		}
		return okResp(), nil
	}}
	w, _ := newTestWebhook(testSeedURL, doer)

	if err := w.Notify(context.Background(), "m"); err != nil {
		t.Fatalf("want success on 2nd attempt, got error: %v", err)
	}
	if got := doer.calls.Load(); got != 2 {
		t.Fatalf("want 2 attempts (1 fail + 1 success), got %d", got)
	}
}

// context cancellation aborts the backoff wait promptly without panicking.
func TestNotify_ContextCancelDuringBackoff(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(500), nil }}
	w, _ := newTestWebhook(testSeedURL, doer)
	w.Backoff = time.Hour // a wait this long must be interrupted by cancel

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(5 * time.Millisecond); cancel() }()

	err := w.Notify(ctx, "m")
	if err == nil {
		t.Fatal("want error on cancellation, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want context.Canceled wrapped, got %v", err)
	}
}

// (d) the full URL is redacted in log output (and in returned errors).
func TestNotify_URLRedactedInLogs(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(500), nil }}
	w, _ := newTestWebhook(testSeedURL, doer)

	err := w.Notify(context.Background(), "m") // fails → logs warn+error
	if err == nil {
		t.Fatal("expected delivery failure to exercise log paths")
	}

	logs := buf.String()
	if logs == "" {
		t.Fatal("expected log output on failed delivery")
	}
	for _, leak := range []string{"SECRET", "TOKEN", "abc123", "/services/"} {
		if strings.Contains(logs, leak) {
			t.Errorf("log output leaked secret fragment %q:\n%s", leak, logs)
		}
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "abc123") {
		t.Errorf("returned error leaked secret: %v", err)
	}
	if !strings.Contains(logs, "hooks.example.com") {
		t.Errorf("expected redacted host in logs, got:\n%s", logs)
	}
}

func TestRedactURL(t *testing.T) {
	tests := []struct {
		raw, want string
	}{
		{"https://hooks.slack.com/services/T00/B00/XXXX", "https://hooks.slack.com"},
		{"http://localhost:9000/hook", "http://localhost:9000"},
		{"://nonsense\x00", "[webhook]"},
		{"", "[webhook]"},
	}
	for _, tt := range tests {
		if got := redactURL(tt.raw); got != tt.want {
			t.Errorf("redactURL(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

// NoOp is a true no-op: returns nil and never touches a transport.
func TestNoOp_NoHTTPAttempt(t *testing.T) {
	var n Notifier = NoOp{}
	if err := n.Notify(context.Background(), "should be dropped"); err != nil {
		t.Fatalf("NoOp.Notify returned %v, want nil", err)
	}
}

// NewNotifier wires production defaults and resolves the seed per send.
func TestNewNotifier_Defaults(t *testing.T) {
	cfg := NewWebhookConfig(testSeedURL)
	w := NewNotifier(cfg)
	if w.MaxAttempts != defaultMaxAttempts || w.Timeout != defaultTimeout || w.Backoff != defaultBackoff {
		t.Errorf("defaults not wired: %+v", w)
	}
	if w.Client == nil {
		t.Error("NewNotifier should wire a default *http.Client")
	}
	if w.Config != cfg {
		t.Error("NewNotifier should retain the supplied holder")
	}
}

// recordingNotifier is an in-memory Notifier that records each call and returns a
// configured error. It needs no real HTTP and lets a test assert that every
// child of a MultiNotifier was invoked and what it received.
type recordingNotifier struct {
	calls   int
	lastMsg string
	err     error
}

func (r *recordingNotifier) Notify(ctx context.Context, message string) error {
	r.calls++
	r.lastMsg = message
	return r.err
}

// Failure isolation + aggregation: a child erroring must not stop later children,
// a healthy child still delivers, and every failure is joined into the result.
func TestMultiNotifier_IsolatesAndAggregates(t *testing.T) {
	errFirst := errors.New("first transport down")
	errThird := errors.New("third transport down")
	first := &recordingNotifier{err: errFirst}
	second := &recordingNotifier{} // healthy
	third := &recordingNotifier{err: errThird}

	m := NewMultiNotifier(first, second, third)
	err := m.Notify(context.Background(), "alert!")

	// Every child invoked exactly once despite the first one erroring.
	if first.calls != 1 || second.calls != 1 || third.calls != 1 {
		t.Fatalf("each child must be called once; got %d/%d/%d", first.calls, second.calls, third.calls)
	}
	// The healthy child still received the message.
	if second.lastMsg != "alert!" {
		t.Errorf("healthy child got %q, want %q", second.lastMsg, "alert!")
	}
	// The aggregate wraps both failures and neither masks the other.
	if err == nil {
		t.Fatal("want aggregated error, got nil")
	}
	if !errors.Is(err, errFirst) {
		t.Errorf("aggregate should wrap first child's error: %v", err)
	}
	if !errors.Is(err, errThird) {
		t.Errorf("aggregate should wrap third child's error: %v", err)
	}
}

// All children succeed → nil aggregate, each delivered.
func TestMultiNotifier_AllSucceed(t *testing.T) {
	a := &recordingNotifier{}
	b := &recordingNotifier{}
	m := NewMultiNotifier(a, b)
	if err := m.Notify(context.Background(), "m"); err != nil {
		t.Fatalf("want nil when all children succeed, got %v", err)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Fatalf("both children must be called; got %d/%d", a.calls, b.calls)
	}
}

// A nil/empty composite is a safe no-op, and nil children are filtered out while
// the remaining live children are still called.
func TestMultiNotifier_NilAndEmptySafe(t *testing.T) {
	if err := NewMultiNotifier().Notify(context.Background(), "m"); err != nil {
		t.Errorf("empty composite should be a no-op, got %v", err)
	}
	good := &recordingNotifier{}
	if err := NewMultiNotifier(nil, good, nil).Notify(context.Background(), "m"); err != nil {
		t.Errorf("nil children must be skipped, got %v", err)
	}
	if good.calls != 1 {
		t.Errorf("the live child should still be called once, got %d", good.calls)
	}
}

// --- Boot-config transports (spec 012, Slice 3) ----------------------------

// jsonResp builds a canned JSON response with a given status code and body — for
// transports (Slack) whose success/failure is partly carried in the body.
func jsonResp(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}
}

// fastTiming shrinks the shared retry budget so transport tests don't burn real
// wall-clock when they exercise the retry path.
func fastTiming() (timeout time.Duration, maxAttempts int, backoff time.Duration) {
	return 50 * time.Millisecond, 3, time.Millisecond
}

// captureLogs redirects slog to a buffer for the duration of the test and returns
// it so a test can assert what was (and was not) logged.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const (
	testSlackToken = "xoxb-SECRET-BOT-TOKEN-9999"
	testTGToken    = "123456789:SECRETTELEGRAMTOKEN"
	testDiscordURL = "https://discord.com/api/webhooks/123456789/SECRETDISCORDTOKEN"
)

func newTestSlackBot(token, channel string, doer HTTPDoer) *SlackBot {
	to, ma, bo := fastTiming()
	return &SlackBot{Token: token, Channel: channel, Client: doer, Timeout: to, MaxAttempts: ma, Backoff: bo}
}

func newTestTelegram(token, chatID string, doer HTTPDoer) *Telegram {
	to, ma, bo := fastTiming()
	return &Telegram{Token: token, ChatID: chatID, Client: doer, Timeout: to, MaxAttempts: ma, Backoff: bo}
}

func newTestDiscord(url string, doer HTTPDoer) *Discord {
	to, ma, bo := fastTiming()
	return &Discord{WebhookURL: url, Client: doer, Timeout: to, MaxAttempts: ma, Backoff: bo}
}

// Slack: correct endpoint, Authorization: Bearer header, and {channel,text} body.
func TestSlackBot_PayloadHeadersURL(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return jsonResp(200, `{"ok":true}`), nil }}
	s := newTestSlackBot(testSlackToken, "#alerts", doer)

	if err := s.Notify(context.Background(), "boom"); err != nil {
		t.Fatalf("Notify: unexpected error: %v", err)
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("want 1 attempt, got %d", doer.calls.Load())
	}
	req := doer.requests[0]
	if req.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", req.Method)
	}
	if req.URL.String() != slackPostMessageURL {
		t.Errorf("posted to %q, want %q", req.URL.String(), slackPostMessageURL)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer "+testSlackToken {
		t.Errorf("Authorization = %q, want Bearer+token", got)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(doer.bodies[0]), &payload); err != nil {
		t.Fatalf("body not JSON: %v (%q)", err, doer.bodies[0])
	}
	if len(payload) != 2 || payload["channel"] != "#alerts" || payload["text"] != "boom" {
		t.Errorf("payload = %v, want {channel:#alerts,text:boom}", payload)
	}
}

// Slack returns HTTP 200 with {"ok":false} on failure — that MUST be an error,
// and the error must surface Slack's own error code.
func TestSlackBot_OKFalseIsError(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) {
		return jsonResp(200, `{"ok":false,"error":"channel_not_found"}`), nil
	}}
	s := newTestSlackBot(testSlackToken, "#nope", doer)

	err := s.Notify(context.Background(), "boom")
	if err == nil {
		t.Fatal("want error on ok:false, got nil")
	}
	if !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("error should surface Slack error code, got %v", err)
	}
	// ok:false is treated like any failed attempt → exhausts the retry budget.
	if got := doer.calls.Load(); got != 3 {
		t.Errorf("want 3 attempts on persistent ok:false, got %d", got)
	}
}

// Slack ok:true on the second attempt recovers — proves the validator failure is
// retried, not fatal on the first try.
func TestSlackBot_OKTrueOnRetry(t *testing.T) {
	doer := &fakeDoer{resp: func(attempt int) (*http.Response, error) {
		if attempt < 2 {
			return jsonResp(200, `{"ok":false,"error":"ratelimited"}`), nil
		}
		return jsonResp(200, `{"ok":true}`), nil
	}}
	s := newTestSlackBot(testSlackToken, "#alerts", doer)
	if err := s.Notify(context.Background(), "boom"); err != nil {
		t.Fatalf("want success on retry, got %v", err)
	}
	if got := doer.calls.Load(); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
}

// Telegram: token in URL path, {chat_id,text} body.
func TestTelegram_PayloadAndURL(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return jsonResp(200, `{"ok":true}`), nil }}
	tg := newTestTelegram(testTGToken, "-100123", doer)

	if err := tg.Notify(context.Background(), "boom"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	req := doer.requests[0]
	wantURL := "https://api.telegram.org/bot" + testTGToken + "/sendMessage"
	if req.URL.String() != wantURL {
		t.Errorf("posted to %q, want %q", req.URL.String(), wantURL)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(doer.bodies[0]), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if len(payload) != 2 || payload["chat_id"] != "-100123" || payload["text"] != "boom" {
		t.Errorf("payload = %v, want {chat_id:-100123,text:boom}", payload)
	}
}

// Discord: {content} body, and a 204 No Content counts as success.
func TestDiscord_PayloadAnd204Success(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(204), nil }}
	d := newTestDiscord(testDiscordURL, doer)

	if err := d.Notify(context.Background(), "boom"); err != nil {
		t.Fatalf("204 should be success, got %v", err)
	}
	if doer.calls.Load() != 1 {
		t.Fatalf("want 1 attempt, got %d", doer.calls.Load())
	}
	req := doer.requests[0]
	if req.URL.String() != testDiscordURL {
		t.Errorf("posted to %q, want %q", req.URL.String(), testDiscordURL)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(doer.bodies[0]), &payload); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if len(payload) != 1 || payload["content"] != "boom" {
		t.Errorf("payload = %v, want {content:boom}", payload)
	}
}

// Each transport's constructor is a no-op (nil Notifier) when its env is unset,
// and even a directly-built transport with empty config makes no HTTP call.
func TestTransports_NoOpWhenUnconfigured(t *testing.T) {
	// Clear every transport env so the constructors see "unset".
	for _, k := range []string{
		"DASHBOARD_SLACK_BOT_TOKEN", "DASHBOARD_SLACK_CHANNEL",
		"DASHBOARD_TELEGRAM_TOKEN", "DASHBOARD_TELEGRAM_CHAT_ID",
		"DASHBOARD_DISCORD_WEBHOOK_URL",
	} {
		t.Setenv(k, "")
	}
	if n := NewSlackBotFromEnv(); n != nil {
		t.Errorf("Slack constructor should return nil when unset, got %v", n)
	}
	if n := NewTelegramFromEnv(); n != nil {
		t.Errorf("Telegram constructor should return nil when unset, got %v", n)
	}
	if n := NewDiscordFromEnv(); n != nil {
		t.Errorf("Discord constructor should return nil when unset, got %v", n)
	}

	// A partially-configured Slack (token only) is also unconfigured.
	t.Setenv("DASHBOARD_SLACK_BOT_TOKEN", testSlackToken)
	if n := NewSlackBotFromEnv(); n != nil {
		t.Errorf("Slack with only a token should be nil, got %v", n)
	}

	// A directly-built transport with empty config must not touch the transport.
	tripwire := &fakeDoer{resp: func(int) (*http.Response, error) {
		t.Fatal("empty-config transport must not make an HTTP call")
		return nil, nil
	}}
	for name, n := range map[string]Notifier{
		"slack":    newTestSlackBot("", "", tripwire),
		"telegram": newTestTelegram("", "", tripwire),
		"discord":  newTestDiscord("", tripwire),
	} {
		if err := n.Notify(context.Background(), "drop"); err != nil {
			t.Errorf("%s empty-config Notify = %v, want nil", name, err)
		}
	}
	if tripwire.calls.Load() != 0 {
		t.Fatalf("want 0 HTTP attempts from empty-config transports, got %d", tripwire.calls.Load())
	}
}

// Constructors enabled only when ALL required env vars are present.
func TestTransports_ConstructorsEnabled(t *testing.T) {
	t.Setenv("DASHBOARD_SLACK_BOT_TOKEN", testSlackToken)
	t.Setenv("DASHBOARD_SLACK_CHANNEL", "#alerts")
	t.Setenv("DASHBOARD_TELEGRAM_TOKEN", testTGToken)
	t.Setenv("DASHBOARD_TELEGRAM_CHAT_ID", "-100123")
	t.Setenv("DASHBOARD_DISCORD_WEBHOOK_URL", testDiscordURL)

	if NewSlackBotFromEnv() == nil {
		t.Error("Slack should be enabled when token+channel set")
	}
	if NewTelegramFromEnv() == nil {
		t.Error("Telegram should be enabled when token+chat id set")
	}
	if NewDiscordFromEnv() == nil {
		t.Error("Discord should be enabled when webhook url set")
	}
}

// Redaction: no token / secret URL fragment may appear in logs or returned errors
// for any of the three transports when delivery fails.
func TestTransports_SecretsRedacted(t *testing.T) {
	transportError := func(int) (*http.Response, error) { return nil, errors.New("dial timeout") }

	cases := []struct {
		name     string
		notifier Notifier
		leaks    []string // fragments that must NOT appear
		wantHost string   // redacted host that SHOULD appear
	}{
		{
			name:     "slack",
			notifier: newTestSlackBot(testSlackToken, "#alerts", &fakeDoer{resp: transportError}),
			leaks:    []string{testSlackToken, "xoxb-"},
			wantHost: "slack.com",
		},
		{
			name:     "telegram",
			notifier: newTestTelegram(testTGToken, "-100123", &fakeDoer{resp: transportError}),
			leaks:    []string{testTGToken, "SECRETTELEGRAMTOKEN"},
			wantHost: "api.telegram.org",
		},
		{
			name:     "discord",
			notifier: newTestDiscord(testDiscordURL, &fakeDoer{resp: transportError}),
			leaks:    []string{"SECRETDISCORDTOKEN", "/webhooks/"},
			wantHost: "discord.com",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := captureLogs(t)
			err := tc.notifier.Notify(context.Background(), "boom")
			if err == nil {
				t.Fatal("expected delivery failure to exercise log paths")
			}
			logs := buf.String()
			if logs == "" {
				t.Fatal("expected log output on failed delivery")
			}
			for _, leak := range tc.leaks {
				if strings.Contains(logs, leak) {
					t.Errorf("logs leaked secret %q:\n%s", leak, logs)
				}
				if strings.Contains(err.Error(), leak) {
					t.Errorf("returned error leaked secret %q: %v", leak, err)
				}
			}
			if !strings.Contains(logs, tc.wantHost) {
				t.Errorf("expected redacted host %q in logs, got:\n%s", tc.wantHost, logs)
			}
		})
	}
}

// Slack's ok:false error string must not carry the bot token either.
func TestSlackBot_OKFalseErrorNoTokenLeak(t *testing.T) {
	doer := &fakeDoer{resp: func(int) (*http.Response, error) {
		return jsonResp(200, `{"ok":false,"error":"invalid_auth"}`), nil
	}}
	s := newTestSlackBot(testSlackToken, "#alerts", doer)
	err := s.Notify(context.Background(), "boom")
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), testSlackToken) || strings.Contains(err.Error(), "xoxb-") {
		t.Errorf("ok:false error leaked token: %v", err)
	}
}

// Fan-out: a MultiNotifier wrapping all three transports (plus the webhook)
// delivers the same message to every transport.
func TestMultiNotifier_FanOutToAllTransports(t *testing.T) {
	slackDoer := &fakeDoer{resp: func(int) (*http.Response, error) { return jsonResp(200, `{"ok":true}`), nil }}
	tgDoer := &fakeDoer{resp: func(int) (*http.Response, error) { return jsonResp(200, `{"ok":true}`), nil }}
	discordDoer := &fakeDoer{resp: func(int) (*http.Response, error) { return statusResp(204), nil }}
	webhookDoer := &fakeDoer{resp: func(int) (*http.Response, error) { return okResp(), nil }}

	webhook, _ := newTestWebhook(testSeedURL, webhookDoer)
	m := NewMultiNotifier(
		webhook,
		newTestSlackBot(testSlackToken, "#alerts", slackDoer),
		newTestTelegram(testTGToken, "-100123", tgDoer),
		newTestDiscord(testDiscordURL, discordDoer),
	)

	if err := m.Notify(context.Background(), "fan out"); err != nil {
		t.Fatalf("fan-out Notify: unexpected error: %v", err)
	}
	for name, d := range map[string]*fakeDoer{
		"webhook": webhookDoer, "slack": slackDoer, "telegram": tgDoer, "discord": discordDoer,
	} {
		if got := d.calls.Load(); got != 1 {
			t.Errorf("%s: want 1 delivery, got %d", name, got)
		}
	}
	// Spot-check one transport actually carried the message.
	var payload map[string]any
	if err := json.Unmarshal([]byte(tgDoer.bodies[0]), &payload); err != nil {
		t.Fatalf("telegram body not JSON: %v", err)
	}
	if payload["text"] != "fan out" {
		t.Errorf("telegram text = %v, want %q", payload["text"], "fan out")
	}
}
