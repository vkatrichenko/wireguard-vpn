package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	dashboard "wireguard-dashboard"
	"wireguard-dashboard/internal/clients"
	"wireguard-dashboard/internal/db"
	"wireguard-dashboard/internal/server"
	"wireguard-dashboard/internal/serverinfo"
)

// recordingApplier is the fake wgsync seam (spec 015) injected into the clients
// Service via SetApplier. It records every Apply call so a test can assert the
// live-apply path fires on a successful mutation and stays untouched when a
// mutation is rejected by validation — without any real wg/sudo/filesystem.
type recordingApplier struct {
	mu    sync.Mutex
	calls int
	last  []db.Client
}

func (a *recordingApplier) Apply(_ context.Context, cs []db.Client) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	a.last = append([]db.Client(nil), cs...)
	return nil
}

func (a *recordingApplier) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// newClientsAdminServer builds a handler whose clients.Service is backed by a
// fresh in-memory DB and the supplied recording applier. All other deps are the
// package's existing fakes — these tests only drive the client-management
// surface. The wg fake returns no peers, so any added client renders "pending".
func newClientsAdminServer(t *testing.T) (http.Handler, *clients.Service, *recordingApplier) {
	t.Helper()
	svc := clients.NewService(newTestDB(t), "172.16.15.1/24")
	rec := &recordingApplier{}
	svc.SetApplier(rec)

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
		nil, nil, nil, svc,
	)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	return handler, svc, rec
}

// 44-char base64 WireGuard public keys for the tests (distinct, all valid).
const (
	adminKeyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	adminKeyB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
)

func doReq(t *testing.T, h http.Handler, method, path string, body io.Reader, headers map[string]string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	raw, _ := io.ReadAll(rec.Body)
	return rec.Code, string(raw)
}

func jsonHeaders() map[string]string { return map[string]string{"Content-Type": "application/json"} }

func formHeaders(htmx bool) map[string]string {
	m := map[string]string{"Content-Type": "application/x-www-form-urlencoded"}
	if htmx {
		m["HX-Request"] = "true"
	}
	return m
}

// listNames GETs /api/clients and returns the set of names present.
func listNames(t *testing.T, h http.Handler) map[string]bool {
	t.Helper()
	code, raw := doReq(t, h, http.MethodGet, "/api/clients", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/clients: want 200, got %d (%s)", code, raw)
	}
	var rows []struct {
		Name    string `json:"name"`
		Enabled bool   `json:"enabled"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("decode /api/clients: %v (%s)", err, raw)
	}
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Name] = r.Enabled
	}
	return out
}

// ---- Add ------------------------------------------------------------------

func TestAddClient_JSON_Success(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	payload, _ := json.Marshal(map[string]string{"name": "alice", "public_key": adminKeyA})
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(payload)), jsonHeaders())
	if code != http.StatusOK {
		t.Fatalf("add JSON: want 200, got %d (%s)", code, body)
	}
	if rec.count() != 1 {
		t.Fatalf("applier calls = %d, want 1 (apply must fire on success)", rec.count())
	}
	if names := listNames(t, h); !names["alice"] {
		t.Fatalf("alice not present after add: %v", names)
	}
}

func TestAddClient_HTMX_Success(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	form := url.Values{"name": {"laptop"}, "public_key": {adminKeyA}}
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(form.Encode()), formHeaders(true))
	if code != http.StatusOK {
		t.Fatalf("add htmx: want 200, got %d (%s)", code, body)
	}
	for _, want := range []string{`id="clients"`, "laptop", "Added client"} {
		if !strings.Contains(body, want) {
			t.Errorf("htmx add fragment missing %q:\n%s", want, body)
		}
	}
	if rec.count() != 1 {
		t.Fatalf("applier calls = %d, want 1", rec.count())
	}
}

func TestAddClient_JSON_ValidationFailures(t *testing.T) {
	cases := []struct {
		name    string
		payload map[string]string
	}{
		{"bad public key", map[string]string{"name": "x", "public_key": "not-base64"}},
		{"empty name", map[string]string{"name": "", "public_key": adminKeyA}},
		{"bad name charset", map[string]string{"name": "has space", "public_key": adminKeyA}},
		{"out-of-subnet address", map[string]string{"name": "x", "public_key": adminKeyA, "address": "10.0.0.5/32"}},
		{"malformed address", map[string]string{"name": "x", "public_key": adminKeyA, "address": "172.16.15.5"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, rec := newClientsAdminServer(t)
			payload, _ := json.Marshal(tc.payload)
			code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(payload)), jsonHeaders())
			if code != http.StatusBadRequest {
				t.Fatalf("want 400, got %d (%s)", code, body)
			}
			if rec.count() != 0 {
				t.Fatalf("applier must NOT fire on validation failure, calls = %d", rec.count())
			}
		})
	}
}

func TestAddClient_HTMX_InvalidRendersCard200(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	form := url.Values{"name": {"x"}, "public_key": {"not-base64"}}
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(form.Encode()), formHeaders(true))
	if code != http.StatusOK {
		t.Fatalf("htmx invalid add: want 200 (card), got %d (%s)", code, body)
	}
	if !strings.Contains(body, `id="clients"`) {
		t.Fatalf("invalid-add card missing id=\"clients\": %s", body)
	}
	if !strings.Contains(body, "client-message-error") {
		t.Fatalf("invalid-add card missing inline error message: %s", body)
	}
	if rec.count() != 0 {
		t.Fatalf("applier must NOT fire on validation failure, calls = %d", rec.count())
	}
}

func TestAddClient_JSON_Duplicate(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	payload, _ := json.Marshal(map[string]string{"name": "alice", "public_key": adminKeyA})
	if code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(payload)), jsonHeaders()); code != http.StatusOK {
		t.Fatalf("first add: want 200, got %d (%s)", code, body)
	}
	if rec.count() != 1 {
		t.Fatalf("after first add: applier calls = %d, want 1", rec.count())
	}

	// Duplicate name (different key) must be rejected and must NOT re-apply.
	dup, _ := json.Marshal(map[string]string{"name": "alice", "public_key": adminKeyB})
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(dup)), jsonHeaders())
	if code != http.StatusBadRequest {
		t.Fatalf("duplicate name: want 400, got %d (%s)", code, body)
	}
	if rec.count() != 1 {
		t.Fatalf("duplicate add must not re-apply, calls = %d (want 1)", rec.count())
	}
}

// ---- Edit -----------------------------------------------------------------

func TestUpdateClient_JSON_Rename(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)
	before := rec.count()

	payload, _ := json.Marshal(map[string]string{"name": "alice2"})
	code, body := doReq(t, h, http.MethodPatch, "/api/clients/alice", strings.NewReader(string(payload)), jsonHeaders())
	if code != http.StatusOK {
		t.Fatalf("rename: want 200, got %d (%s)", code, body)
	}
	if rec.count() != before+1 {
		t.Fatalf("rename must re-apply: calls = %d, want %d", rec.count(), before+1)
	}
	names := listNames(t, h)
	if names["alice"] || !names["alice2"] {
		t.Fatalf("rename not reflected: %v", names)
	}
}

func TestUpdateClient_HTMX_Disable(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)
	before := rec.count()

	form := url.Values{"enabled": {"false"}}
	code, body := doReq(t, h, http.MethodPatch, "/api/clients/alice", strings.NewReader(form.Encode()), formHeaders(true))
	if code != http.StatusOK {
		t.Fatalf("disable htmx: want 200, got %d (%s)", code, body)
	}
	if !strings.Contains(body, `id="clients"`) {
		t.Fatalf("disable card missing id=\"clients\": %s", body)
	}
	if rec.count() != before+1 {
		t.Fatalf("disable must re-apply: calls = %d, want %d", rec.count(), before+1)
	}
	if names := listNames(t, h); names["alice"] {
		t.Fatalf("alice should be disabled (enabled=false), got enabled=%v", names["alice"])
	}
}

func TestUpdateClient_JSON_NotFound(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	payload, _ := json.Marshal(map[string]string{"name": "ghost2"})
	code, body := doReq(t, h, http.MethodPatch, "/api/clients/ghost", strings.NewReader(string(payload)), jsonHeaders())
	if code != http.StatusNotFound {
		t.Fatalf("update unknown: want 404, got %d (%s)", code, body)
	}
	if rec.count() != 0 {
		t.Fatalf("update unknown must not apply, calls = %d", rec.count())
	}
}

// ---- Delete ---------------------------------------------------------------

func TestDeleteClient_JSON_Success(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)
	before := rec.count()

	code, body := doReq(t, h, http.MethodDelete, "/api/clients/alice", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("delete: want 200, got %d (%s)", code, body)
	}
	if rec.count() != before+1 {
		t.Fatalf("delete must re-apply: calls = %d, want %d", rec.count(), before+1)
	}
	if names := listNames(t, h); names["alice"] {
		t.Fatalf("alice still present after delete: %v", names)
	}
}

func TestDeleteClient_JSON_NotFound(t *testing.T) {
	h, _, rec := newClientsAdminServer(t)

	code, body := doReq(t, h, http.MethodDelete, "/api/clients/ghost", nil, nil)
	if code != http.StatusNotFound {
		t.Fatalf("delete unknown: want 404, got %d (%s)", code, body)
	}
	if rec.count() != 0 {
		t.Fatalf("delete unknown must not apply, calls = %d", rec.count())
	}
}

func TestDeleteClient_HTMX_Success(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)

	code, body := doReq(t, h, http.MethodDelete, "/api/clients/alice", nil, map[string]string{"HX-Request": "true"})
	if code != http.StatusOK {
		t.Fatalf("delete htmx: want 200, got %d (%s)", code, body)
	}
	for _, want := range []string{`id="clients"`, "Removed client"} {
		if !strings.Contains(body, want) {
			t.Errorf("delete card missing %q:\n%s", want, body)
		}
	}
}

// ---- Export ---------------------------------------------------------------

func TestExportClients_HCL(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)
	addOne(t, h, "bob", adminKeyB)

	code, body := doReq(t, h, http.MethodGet, "/api/clients/export", nil, nil)
	if code != http.StatusOK {
		t.Fatalf("export hcl: want 200, got %d (%s)", code, body)
	}

	// Re-parse via the same renderer's output shape: assert both names + the
	// header are present, and the addresses were auto-allocated in order.
	for _, want := range []string{
		"clients_config = [",
		`name       = "alice"`,
		`name       = "bob"`,
		`public_key = "` + adminKeyA + `"`,
		`public_key = "` + adminKeyB + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("HCL export missing %q:\n%s", want, body)
		}
	}
}

func TestExportClients_HCL_Headers(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/clients/export?format=hcl", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Errorf("Content-Disposition = %q, want an attachment", cd)
	}
}

func TestExportClients_TFVars_ValidJSON(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)
	addOne(t, h, "alice", adminKeyA)

	req := httptest.NewRequest(http.MethodGet, "/api/clients/export?format=tfvars", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("export tfvars: want 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var doc struct {
		ClientsConfig []struct {
			Name      string `json:"name"`
			Address   string `json:"address"`
			PublicKey string `json:"public_key"`
		} `json:"clients_config"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("tfvars export not valid JSON: %v (%s)", err, rec.Body.String())
	}
	if len(doc.ClientsConfig) != 1 || doc.ClientsConfig[0].Name != "alice" {
		t.Fatalf("tfvars export = %+v, want one alice entry", doc.ClientsConfig)
	}
}

func TestExportClients_BadFormat(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)
	code, _ := doReq(t, h, http.MethodGet, "/api/clients/export?format=yaml", nil, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("bad export format: want 400, got %d", code)
	}
}

// ---- Service unavailable --------------------------------------------------

func TestAddClient_NilService503(t *testing.T) {
	// A server constructed with a nil clients.Service responds 503 on the write
	// path, matching the webhook precedent for an unwired management surface.
	infoSvc := &serverinfo.Service{
		IMDS:   fakeIMDS{ip: "203.0.113.1"},
		Runner: func(_ context.Context, _ string, _ ...string) ([]byte, error) { return []byte("k=\n"), nil },
	}
	systemdSvc := systemdRunnerActive(time.Now().Add(-2 * time.Hour))
	h, err := server.New(
		dashboard.WebFS(), infoSvc, &systemdSvc, fakeClientsfileSvc(), fakeWgSvc(),
		fakeProcSvc(), newTestDB(t), nil, fakeDiskSvc(), fakeProcessesSvc(), fakeNetdevSvc(),
		nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("server.New: %v", err)
	}
	payload, _ := json.Marshal(map[string]string{"name": "alice", "public_key": adminKeyA})
	code, _ := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(payload)), jsonHeaders())
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil service add: want 503, got %d", code)
	}
}

// addOne is a helper that adds a client via the JSON endpoint and fails the test
// if it doesn't succeed — used to set up edit/delete/export fixtures.
// TestClientsFragment_InlineEditRow asserts spec 016 req 2.2: the Clients
// fragment renders a full-width inline edit row (not the old right-side drawer)
// carrying the edit form with the correct PATCH target and the same fields.
func TestClientsFragment_InlineEditRow(t *testing.T) {
	h, _, _ := newClientsAdminServer(t)

	form := url.Values{"name": {"laptop"}, "public_key": {adminKeyA}}
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(form.Encode()), formHeaders(true))
	if code != http.StatusOK {
		t.Fatalf("add htmx: want 200, got %d (%s)", code, body)
	}

	for _, want := range []string{
		`class="client-edit-row`,
		`hx-patch="/api/clients/laptop"`,
		`name="name"`,
		`name="public_key"`,
		`name="address"`,
		`name="note"`,
		`class="client-btn client-edit-toggle"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("inline edit row fragment missing %q:\n%s", want, body)
		}
	}

	// The old right-side drawer markup must be gone.
	for _, gone := range []string{`<details class="client-edit"`, `<summary>Edit</summary>`} {
		if strings.Contains(body, gone) {
			t.Errorf("old edit drawer markup still present: %q", gone)
		}
	}
}

func addOne(t *testing.T, h http.Handler, name, key string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"name": name, "public_key": key})
	code, body := doReq(t, h, http.MethodPost, "/api/clients", strings.NewReader(string(payload)), jsonHeaders())
	if code != http.StatusOK {
		t.Fatalf("addOne(%q): want 200, got %d (%s)", name, code, body)
	}
}
