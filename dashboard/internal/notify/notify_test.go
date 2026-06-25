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
