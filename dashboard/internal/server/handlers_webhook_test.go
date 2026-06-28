package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/notify"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// The secret path/token used in test webhook URLs. Every response-body assertion
// checks this never appears, proving the masked-only contract holds.
const webhookSecretPath = "T00000000/B11111111/abcdefghijklmnopqrstuvwx"

func sampleWebhookURL() string {
	return "https://hooks.slack.com/services/" + webhookSecretPath
}

// newWebhookServer builds a handler wired with the given webhook holder (nil to
// exercise the disabled / 503 paths). All other deps are the package's existing
// fakes — these tests only touch the webhook + alert surfaces.
func newWebhookServer(t *testing.T, cfg *notify.WebhookConfig) http.Handler {
	t.Helper()
	infoSvc := &serverinfo.Service{
		IMDS: fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJK=\n"), nil
		},
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))
	handler, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil,
		cfg,
		nil,
	)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler
}

type webhookStatusBody struct {
	Enabled        bool   `json:"enabled"`
	CurrentMasked  string `json:"current_masked"`
	OverrideActive bool   `json:"override_active"`
}

func getWebhook(t *testing.T, h http.Handler) (int, string, webhookStatusBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/webhook", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var body webhookStatusBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode GET /api/webhook: %v (body=%s)", err, raw)
		}
	}
	return rec.Code, string(raw), body
}

func postWebhookForm(t *testing.T, h http.Handler, value string) (int, string, webhookStatusBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook",
		strings.NewReader(url.Values{"url": {value}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var body webhookStatusBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode POST /api/webhook: %v (body=%s)", err, raw)
		}
	}
	return rec.Code, string(raw), body
}

func postWebhookRevert(t *testing.T, h http.Handler) (int, string, webhookStatusBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/revert", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var body webhookStatusBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode POST /api/webhook/revert: %v (body=%s)", err, raw)
		}
	}
	return rec.Code, string(raw), body
}

func assertNoSecretLeak(t *testing.T, where, body string) {
	t.Helper()
	if strings.Contains(body, webhookSecretPath) {
		t.Fatalf("%s: response body leaked the secret webhook path: %s", where, body)
	}
}

func TestGetWebhook_DisabledNoSeed(t *testing.T) {
	h := newWebhookServer(t, notify.NewWebhookConfig(""))
	code, raw, body := getWebhook(t, h)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if body.Enabled || body.CurrentMasked != "" || body.OverrideActive {
		t.Fatalf("empty seed should be disabled/empty, got %+v", body)
	}
	assertNoSecretLeak(t, "GET disabled", raw)
}

func TestGetWebhook_SeededMaskedOnly(t *testing.T) {
	h := newWebhookServer(t, notify.NewWebhookConfig(sampleWebhookURL()))
	code, raw, body := getWebhook(t, h)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}
	if !body.Enabled {
		t.Fatalf("seeded holder should report enabled=true")
	}
	if body.CurrentMasked != "https://hooks.slack.com" {
		t.Fatalf("masked url = %q, want scheme+host only", body.CurrentMasked)
	}
	if body.OverrideActive {
		t.Fatalf("seed-only should not report override_active")
	}
	assertNoSecretLeak(t, "GET seeded", raw)
}

func TestGetWebhook_NilHolderDisabled(t *testing.T) {
	h := newWebhookServer(t, nil)
	code, raw, body := getWebhook(t, h)
	if code != http.StatusOK {
		t.Fatalf("nil holder GET should be 200, got %d", code)
	}
	if body.Enabled || body.CurrentMasked != "" || body.OverrideActive {
		t.Fatalf("nil holder should report disabled/empty, got %+v", body)
	}
	assertNoSecretLeak(t, "GET nil", raw)
}

func TestPostWebhook_ValidSetsOverride(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	code, raw, body := postWebhookForm(t, h, sampleWebhookURL())
	if code != http.StatusOK {
		t.Fatalf("valid https url: want 200, got %d (body=%s)", code, raw)
	}
	if !body.Enabled || !body.OverrideActive {
		t.Fatalf("after set: want enabled+override_active, got %+v", body)
	}
	if body.CurrentMasked != "https://hooks.slack.com" {
		t.Fatalf("masked url = %q, want scheme+host only", body.CurrentMasked)
	}
	assertNoSecretLeak(t, "POST valid", raw)

	if got := cfg.Current(); got != sampleWebhookURL() {
		t.Fatalf("holder.Current() = %q, want the full set URL", got)
	}
}

func TestPostWebhook_RejectsHTTP(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	bad := "http://hooks.slack.com/services/" + webhookSecretPath
	code, raw, _ := postWebhookForm(t, h, bad)
	if code != http.StatusBadRequest {
		t.Fatalf("http:// url: want 400, got %d", code)
	}
	if cfg.Enabled() {
		t.Fatalf("rejected url must not mutate the holder")
	}
	assertNoSecretLeak(t, "POST http reject", raw)
}

func TestPostWebhook_RejectsMalformed(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	for _, bad := range []string{"", "not a url", "ftp://example.com/x", "https://"} {
		code, raw, _ := postWebhookForm(t, h, bad)
		if code != http.StatusBadRequest {
			t.Fatalf("malformed %q: want 400, got %d (body=%s)", bad, code, raw)
		}
		if cfg.Enabled() {
			t.Fatalf("malformed %q must not enable the holder", bad)
		}
	}
}

func TestPostWebhook_JSONBody(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	payload, _ := json.Marshal(map[string]string{"url": sampleWebhookURL()})
	req := httptest.NewRequest(http.MethodPost, "/api/webhook", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK {
		t.Fatalf("json body: want 200, got %d (body=%s)", rec.Code, raw)
	}
	assertNoSecretLeak(t, "POST json", string(raw))
	if cfg.Current() != sampleWebhookURL() {
		t.Fatalf("json body did not set the holder: %q", cfg.Current())
	}
}

func TestPostWebhook_NilHolder503(t *testing.T) {
	h := newWebhookServer(t, nil)
	code, _, _ := postWebhookForm(t, h, sampleWebhookURL())
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil holder POST: want 503, got %d", code)
	}
}

func TestPostWebhookRevert_RestoresSeed(t *testing.T) {
	cfg := notify.NewWebhookConfig(sampleWebhookURL())
	h := newWebhookServer(t, cfg)

	// Override to disabled, then revert back to the seed.
	if code, _, _ := postWebhookForm(t, h, "https://other.example.com/path"); code != http.StatusOK {
		t.Fatalf("set override: want 200, got %d", code)
	}
	code, raw, body := postWebhookRevert(t, h)
	if code != http.StatusOK {
		t.Fatalf("revert: want 200, got %d", code)
	}
	if body.OverrideActive {
		t.Fatalf("after revert, override_active should be false")
	}
	if cfg.Current() != sampleWebhookURL() {
		t.Fatalf("revert did not restore the seed: %q", cfg.Current())
	}
	assertNoSecretLeak(t, "POST revert", raw)
}

func TestPostWebhookRevert_NilHolder503(t *testing.T) {
	h := newWebhookServer(t, nil)
	code, _, _ := postWebhookRevert(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil holder revert: want 503, got %d", code)
	}
}

type webhookTestBody struct {
	Delivered bool   `json:"delivered"`
	Reason    string `json:"reason"`
}

func postWebhookTest(t *testing.T, h http.Handler) (int, string, webhookTestBody) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/webhook/test", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	var body webhookTestBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode POST /api/webhook/test: %v (body=%s)", err, raw)
		}
	}
	return rec.Code, string(raw), body
}

// TestPostWebhookTest_Delivered points the holder at a 200-returning httptest
// server and asserts a single {"text":...} POST is received and delivered:true
// is reported. The holder is Set directly (bypassing the https-only handler
// validation) because we're exercising the SEND path, not the validator.
func TestPostWebhookTest_Delivered(t *testing.T) {
	var hits int
	var gotText bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Method != http.MethodPost {
			t.Errorf("webhook receiver: want POST, got %s", r.Method)
		}
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode webhook body: %v", err)
		}
		if payload.Text != "" {
			gotText = true
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := notify.NewWebhookConfig("")
	cfg.Set(ts.URL)
	h := newWebhookServer(t, cfg)

	code, raw, body := postWebhookTest(t, h)
	if code != http.StatusOK {
		t.Fatalf("test send: want 200, got %d (body=%s)", code, raw)
	}
	if !body.Delivered {
		t.Fatalf("want delivered=true, got %+v", body)
	}
	if hits != 1 {
		t.Fatalf("want exactly 1 POST to the receiver, got %d", hits)
	}
	if !gotText {
		t.Fatalf("receiver did not see a non-empty {\"text\":...} body")
	}
}

// TestPostWebhookTest_Failed points the holder at a 500-returning server and
// asserts delivered:false with a fixed reason that leaks no part of the URL.
// The notifier retries a few times; we only assert the final outcome.
func TestPostWebhookTest_Failed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	// A recognisable secret segment in the URL the response must never echo.
	secretURL := ts.URL + "/services/" + webhookSecretPath
	cfg := notify.NewWebhookConfig("")
	cfg.Set(secretURL)
	h := newWebhookServer(t, cfg)

	code, raw, body := postWebhookTest(t, h)
	if code != http.StatusOK {
		t.Fatalf("test send (failing): want 200, got %d (body=%s)", code, raw)
	}
	if body.Delivered {
		t.Fatalf("want delivered=false on a 500 receiver, got %+v", body)
	}
	if body.Reason == "" {
		t.Fatalf("failed test send should carry a reason, got %+v", body)
	}
	assertNoSecretLeak(t, "POST test failed", raw)
	// Also assert no part of the host/URL appears in the reason.
	if strings.Contains(raw, ts.URL) || strings.Contains(raw, "/services/") {
		t.Fatalf("test response leaked URL/host detail: %s", raw)
	}
}

// TestPostWebhookTest_NotConfigured asserts an empty holder yields
// delivered:false with the "no webhook configured" reason and that NO HTTP is
// attempted (a receiver wired but left unreferenced gets zero hits).
func TestPostWebhookTest_NotConfigured(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := notify.NewWebhookConfig("")
	cfg.Set("") // explicit OFF override; holder stays not-Enabled
	h := newWebhookServer(t, cfg)

	code, raw, body := postWebhookTest(t, h)
	if code != http.StatusOK {
		t.Fatalf("test send (not configured): want 200, got %d (body=%s)", code, raw)
	}
	if body.Delivered {
		t.Fatalf("want delivered=false when no URL configured, got %+v", body)
	}
	if body.Reason != "no webhook configured" {
		t.Fatalf("want reason=%q, got %q", "no webhook configured", body.Reason)
	}
	if hits != 0 {
		t.Fatalf("no URL configured must attempt NO HTTP, got %d hits", hits)
	}
}

// TestPostWebhookTest_NilHolder503 asserts a server built with a nil holder
// responds 503 without panicking.
func TestPostWebhookTest_NilHolder503(t *testing.T) {
	h := newWebhookServer(t, nil)
	code, _, _ := postWebhookTest(t, h)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil holder test send: want 503, got %d", code)
	}
}

// ---- Slice 4: htmx card content-negotiation -------------------------------

// postWebhookHTMX issues a form POST with HX-Request: true and returns the
// status code + raw HTML body. Used to exercise the card-fragment branch of
// the set/test/revert handlers.
func postWebhookHTMX(t *testing.T, h http.Handler, path string, form url.Values) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(http.MethodPost, path, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	return rec.Code, string(raw)
}

func TestSetWebhook_HTMX_ValidRendersCard(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	code, raw := postWebhookHTMX(t, h, "/api/webhook", url.Values{"url": {sampleWebhookURL()}})
	if code != http.StatusOK {
		t.Fatalf("HTMX set valid: want 200, got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, `id="webhook-card"`) {
		t.Fatalf("card fragment missing id=\"webhook-card\": %s", raw)
	}
	if !strings.Contains(raw, "https://hooks.slack.com") {
		t.Fatalf("card should show the masked host, got: %s", raw)
	}
	if !strings.Contains(raw, "Saved.") {
		t.Fatalf("card should show the success message, got: %s", raw)
	}
	assertNoSecretLeak(t, "HTMX set valid", raw)
	if cfg.Current() != sampleWebhookURL() {
		t.Fatalf("HTMX set did not persist the URL: %q", cfg.Current())
	}
}

func TestSetWebhook_HTMX_InvalidRendersCard200(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	// http:// is rejected; the HTMX path returns 200 with the card + error so
	// htmx (which won't swap a 4xx) shows the reason inline.
	bad := "http://hooks.slack.com/services/" + webhookSecretPath
	code, raw := postWebhookHTMX(t, h, "/api/webhook", url.Values{"url": {bad}})
	if code != http.StatusOK {
		t.Fatalf("HTMX set invalid: want 200 (card), got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, `id="webhook-card"`) {
		t.Fatalf("invalid-set card missing id=\"webhook-card\": %s", raw)
	}
	if !strings.Contains(raw, "https") {
		t.Fatalf("error card should mention https requirement, got: %s", raw)
	}
	if cfg.Enabled() {
		t.Fatalf("rejected url must not mutate the holder")
	}
	assertNoSecretLeak(t, "HTMX set invalid", raw)
}

func TestTestWebhook_HTMX_Delivered(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := notify.NewWebhookConfig("")
	cfg.Set(ts.URL)
	h := newWebhookServer(t, cfg)

	code, raw := postWebhookHTMX(t, h, "/api/webhook/test", nil)
	if code != http.StatusOK {
		t.Fatalf("HTMX test delivered: want 200, got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, `id="webhook-card"`) {
		t.Fatalf("test card missing id=\"webhook-card\": %s", raw)
	}
	if !strings.Contains(raw, "Test alert delivered.") {
		t.Fatalf("delivered card should show the success message, got: %s", raw)
	}
}

func TestTestWebhook_HTMX_Failed(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := notify.NewWebhookConfig("")
	cfg.Set(ts.URL + "/services/" + webhookSecretPath)
	h := newWebhookServer(t, cfg)

	code, raw := postWebhookHTMX(t, h, "/api/webhook/test", nil)
	if code != http.StatusOK {
		t.Fatalf("HTMX test failed: want 200, got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, "Delivery failed.") {
		t.Fatalf("failed card should show the failure message, got: %s", raw)
	}
	// The card intentionally shows the MASKED host (scheme+host) in its status
	// line — that's the same masking shown everywhere. What must never leak is
	// the secret PATH/token, asserted via assertNoSecretLeak. (A naive
	// "/services/" check would false-positive on the empty input's placeholder
	// text, which legitimately reads "https://hooks.slack.com/services/…".)
	assertNoSecretLeak(t, "HTMX test failed", raw)
}

func TestTestWebhook_HTMX_NotConfigured(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	code, raw := postWebhookHTMX(t, h, "/api/webhook/test", nil)
	if code != http.StatusOK {
		t.Fatalf("HTMX test not-configured: want 200, got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, "No webhook configured.") {
		t.Fatalf("not-configured card should show the info message, got: %s", raw)
	}
}

func TestRevertWebhook_HTMX_RendersCard(t *testing.T) {
	cfg := notify.NewWebhookConfig(sampleWebhookURL())
	h := newWebhookServer(t, cfg)

	if code, _, _ := postWebhookForm(t, h, "https://other.example.com/path"); code != http.StatusOK {
		t.Fatalf("set override: want 200, got %d", code)
	}
	code, raw := postWebhookHTMX(t, h, "/api/webhook/revert", nil)
	if code != http.StatusOK {
		t.Fatalf("HTMX revert: want 200, got %d (body=%s)", code, raw)
	}
	if !strings.Contains(raw, `id="webhook-card"`) {
		t.Fatalf("revert card missing id=\"webhook-card\": %s", raw)
	}
	if !strings.Contains(raw, "Reverted to deploy value.") {
		t.Fatalf("revert card should show the revert message, got: %s", raw)
	}
	if cfg.Current() != sampleWebhookURL() {
		t.Fatalf("revert did not restore the seed: %q", cfg.Current())
	}
	assertNoSecretLeak(t, "HTMX revert", raw)
}

// getAbout renders the full About tab fragment so we can assert the webhook
// card is present in its various states without driving every IMDS read.
func getAbout(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/partial/about", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /partial/about: want 200, got %d", rec.Code)
	}
	raw, _ := io.ReadAll(rec.Body)
	return string(raw)
}

func TestAboutTab_WebhookCard_NotConfigured(t *testing.T) {
	h := newWebhookServer(t, notify.NewWebhookConfig(""))
	body := getAbout(t, h)
	if !strings.Contains(body, `id="webhook-card"`) {
		t.Fatalf("About tab missing webhook card: %s", body)
	}
	if !strings.Contains(body, "Not configured") {
		t.Fatalf("empty holder should render the not-configured state")
	}
	if !strings.Contains(body, `hx-post="/api/webhook"`) {
		t.Fatalf("card missing the Set form (hx-post=/api/webhook)")
	}
	if !strings.Contains(body, `hx-post="/api/webhook/test"`) {
		t.Fatalf("card missing the Test control")
	}
	if !strings.Contains(body, `hx-post="/api/webhook/revert"`) {
		t.Fatalf("card missing the Revert control")
	}
	assertNoSecretLeak(t, "About not-configured", body)
}

func TestAboutTab_WebhookCard_EnabledMaskedNoSecret(t *testing.T) {
	h := newWebhookServer(t, notify.NewWebhookConfig(sampleWebhookURL()))
	body := getAbout(t, h)
	if !strings.Contains(body, "https://hooks.slack.com") {
		t.Fatalf("enabled card should show the masked host: %s", body)
	}
	// Seed-only: no override badge.
	if strings.Contains(body, "runtime override") {
		t.Fatalf("seed-only holder should not render the override badge")
	}
	assertNoSecretLeak(t, "About enabled", body)
}

func TestAboutTab_WebhookCard_OverrideBadge(t *testing.T) {
	cfg := notify.NewWebhookConfig("https://seed.example.com/deploy")
	cfg.Set(sampleWebhookURL()) // runtime override shadows the seed
	h := newWebhookServer(t, cfg)
	body := getAbout(t, h)
	if !strings.Contains(body, "runtime override") {
		t.Fatalf("override-active holder should render the override badge: %s", body)
	}
	assertNoSecretLeak(t, "About override", body)
}

// TestWebhook_DynamicAlertsEnabled proves /api/alerts reflects the LIVE webhook
// state (spec 008 Slice 2): disabled with an empty webhook, enabled after a Set,
// disabled again after a Revert to an empty seed.
func TestWebhook_DynamicAlertsEnabled(t *testing.T) {
	cfg := notify.NewWebhookConfig("")
	h := newWebhookServer(t, cfg)

	getAlertsEnabled := func() bool {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/alerts", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/alerts: want 200, got %d", rec.Code)
		}
		var body struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("decode /api/alerts: %v", err)
		}
		return body.Enabled
	}

	if getAlertsEnabled() {
		t.Fatalf("empty webhook should report alerts enabled=false")
	}
	if code, _, _ := postWebhookForm(t, h, sampleWebhookURL()); code != http.StatusOK {
		t.Fatalf("set webhook: want 200, got %d", code)
	}
	if !getAlertsEnabled() {
		t.Fatalf("after Set, alerts should report enabled=true")
	}
	if code, _, _ := postWebhookRevert(t, h); code != http.StatusOK {
		t.Fatalf("revert webhook: want 200, got %d", code)
	}
	if getAlertsEnabled() {
		t.Fatalf("after revert to empty seed, alerts should report enabled=false")
	}
}
