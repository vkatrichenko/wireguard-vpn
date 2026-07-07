package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// recordedRequest captures one request the fake dashboard received, so tests
// can assert both "how many requests" (zero for a rejected confirm-gate) and
// "the right one" (method/path/body for an accepted call).
type recordedRequest struct {
	Method string
	Path   string
	Body   string
}

// fakeDashboard is an httptest-backed stand-in for the real dashboard's
// /api/clients* surface. It records every request it receives and returns a
// canned response, so tests never need a real dashboard binary or SQLite —
// exactly the "unit/local testing" scope the task calls for, not Phase 4's
// live-tunnel validation.
type fakeDashboard struct {
	mu       sync.Mutex
	requests []recordedRequest

	// clientsResponse is served verbatim for GET /api/clients, letting each
	// test control what preview_delete_client sees.
	clientsResponse string
}

func newFakeDashboard(clientsJSON string) (*fakeDashboard, *dashboard.Client, func()) {
	f := &fakeDashboard{clientsResponse: clientsJSON}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, recordedRequest{Method: r.Method, Path: r.URL.Path, Body: string(body)})
		f.mu.Unlock()

		if r.Method == http.MethodGet && r.URL.Path == "/api/clients" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(f.clientsResponse))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	addr := strings.TrimPrefix(srv.URL, "http://")
	client := dashboard.New(addr)
	return f, client, srv.Close
}

func (f *fakeDashboard) all() []recordedRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedRequest, len(f.requests))
	copy(out, f.requests)
	return out
}

func (f *fakeDashboard) countMethod(method string) int {
	n := 0
	for _, r := range f.all() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// newTestServer wires RegisterMutating (and, since it's the same package
// entry point real callers use, nothing else) onto an in-memory MCP client/
// server pair, returning a CallTool function scoped to ctx/t.
func newTestServer(t *testing.T, client *dashboard.Client, store *Store) func(name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.0"}, nil)
	RegisterMutating(server, client, store)

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := mcpClient.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })

	return func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("CallTool(%s): transport-level error: %v", name, err)
		}
		return res
	}
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

var tokenRE = regexp.MustCompile(`token="([0-9a-f]+)"`)

func extractToken(text string) string {
	m := tokenRE.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestAddClientConfirmGateBlocksWithoutConfirm(t *testing.T) {
	fake, client, closeSrv := newFakeDashboard(`[]`)
	defer closeSrv()
	call := newTestServer(t, client, NewStore())

	res := call("add_client", map[string]any{"name": "alice", "public_key": "pk=="})
	if !res.IsError {
		t.Fatalf("expected IsError for confirm=false, got result: %+v", res)
	}
	if got := len(fake.all()); got != 0 {
		t.Fatalf("expected zero requests to the dashboard, got %d: %+v", got, fake.all())
	}
}

func TestAddClientConfirmTrueSendsOneRequest(t *testing.T) {
	fake, client, closeSrv := newFakeDashboard(`[]`)
	defer closeSrv()
	call := newTestServer(t, client, NewStore())

	res := call("add_client", map[string]any{
		"name": "alice", "public_key": "pk==", "address": "10.10.0.5/32", "note": "laptop", "confirm": true,
	})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}

	reqs := fake.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one request, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != http.MethodPost || reqs[0].Path != "/api/clients" {
		t.Fatalf("expected POST /api/clients, got %s %s", reqs[0].Method, reqs[0].Path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if body["name"] != "alice" || body["public_key"] != "pk==" || body["address"] != "10.10.0.5/32" || body["note"] != "laptop" {
		t.Fatalf("unexpected request body: %v", body)
	}
}

func TestEditClientConfirmGate(t *testing.T) {
	fake, client, closeSrv := newFakeDashboard(`[]`)
	defer closeSrv()
	call := newTestServer(t, client, NewStore())

	if res := call("edit_client", map[string]any{"name": "alice", "note": "new note"}); !res.IsError {
		t.Fatalf("expected IsError for confirm=false")
	}
	if got := len(fake.all()); got != 0 {
		t.Fatalf("expected zero requests, got %d", got)
	}

	res := call("edit_client", map[string]any{"name": "alice", "note": "new note", "confirm": true})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	reqs := fake.all()
	if len(reqs) != 1 {
		t.Fatalf("expected exactly one request, got %d: %+v", len(reqs), reqs)
	}
	if reqs[0].Method != http.MethodPatch || reqs[0].Path != "/api/clients/alice" {
		t.Fatalf("expected PATCH /api/clients/alice, got %s %s", reqs[0].Method, reqs[0].Path)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
	if body["note"] != "new note" {
		t.Fatalf("expected note in PATCH body, got %v", body)
	}
	if _, present := body["public_key"]; present {
		t.Fatalf("expected omitted public_key to be absent from PATCH body, got %v", body)
	}
}

func TestEnableDisableClientConfirmGate(t *testing.T) {
	for _, tc := range []struct {
		tool    string
		enabled bool
	}{
		{"enable_client", true},
		{"disable_client", false},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			fake, client, closeSrv := newFakeDashboard(`[]`)
			defer closeSrv()
			call := newTestServer(t, client, NewStore())

			if res := call(tc.tool, map[string]any{"name": "alice"}); !res.IsError {
				t.Fatalf("expected IsError for confirm=false")
			}
			if got := len(fake.all()); got != 0 {
				t.Fatalf("expected zero requests, got %d", got)
			}

			res := call(tc.tool, map[string]any{"name": "alice", "confirm": true})
			if res.IsError {
				t.Fatalf("unexpected error result: %s", resultText(res))
			}
			reqs := fake.all()
			if len(reqs) != 1 {
				t.Fatalf("expected exactly one request, got %d: %+v", len(reqs), reqs)
			}
			if reqs[0].Method != http.MethodPatch || reqs[0].Path != "/api/clients/alice" {
				t.Fatalf("expected PATCH /api/clients/alice, got %s %s", reqs[0].Method, reqs[0].Path)
			}
			var body map[string]any
			if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
				t.Fatalf("decoding request body: %v", err)
			}
			if body["enabled"] != tc.enabled {
				t.Fatalf("expected enabled=%v, got %v", tc.enabled, body["enabled"])
			}
		})
	}
}

func TestPreviewDeleteClientIssuesTokenWithoutMutating(t *testing.T) {
	clientsJSON := `[{"name":"alice","public_key":"pk==","status":"online","enabled":true}]`
	fake, client, closeSrv := newFakeDashboard(clientsJSON)
	defer closeSrv()
	call := newTestServer(t, client, NewStore())

	res := call("preview_delete_client", map[string]any{"name": "alice"})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	if got := fake.countMethod(http.MethodDelete); got != 0 {
		t.Fatalf("preview_delete_client must never DELETE, got %d DELETE calls", got)
	}
	text := resultText(res)
	if extractToken(text) == "" {
		t.Fatalf("expected a token embedded in the preview response, got: %s", text)
	}
}

func TestPreviewDeleteClientUnknownNameIssuesNoToken(t *testing.T) {
	fake, client, closeSrv := newFakeDashboard(`[]`)
	defer closeSrv()
	store := NewStore()
	call := newTestServer(t, client, store)

	res := call("preview_delete_client", map[string]any{"name": "ghost"})
	if !res.IsError {
		t.Fatalf("expected an error for an unknown peer name")
	}
	if got := len(fake.all()); got != 1 { // the GET /api/clients lookup itself
		t.Fatalf("expected exactly one GET request, got %d", got)
	}
	// No token should have been issued: Verify must fail even with an empty
	// token guess.
	if store.Verify("ghost", "") {
		t.Fatalf("expected no token to have been issued for an unknown peer")
	}
}

func TestDeleteClientRequiresValidToken(t *testing.T) {
	clientsJSON := `[{"name":"alice","public_key":"pk==","status":"online","enabled":true}]`
	fake, client, closeSrv := newFakeDashboard(clientsJSON)
	defer closeSrv()
	store := NewStore()
	call := newTestServer(t, client, store)

	// Wrong/missing token: no DELETE reaches the dashboard.
	if res := call("delete_client", map[string]any{"name": "alice", "token": "bogus"}); !res.IsError {
		t.Fatalf("expected IsError for a bogus token")
	}
	if got := fake.countMethod(http.MethodDelete); got != 0 {
		t.Fatalf("expected zero DELETE calls for a bad token, got %d", got)
	}

	// Preview to get a real token, then redeem it.
	preview := call("preview_delete_client", map[string]any{"name": "alice"})
	token := extractToken(resultText(preview))
	if token == "" {
		t.Fatalf("expected a token from preview_delete_client")
	}

	res := call("delete_client", map[string]any{"name": "alice", "token": token})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", resultText(res))
	}
	reqs := fake.all()
	deletes := 0
	for _, r := range reqs {
		if r.Method == http.MethodDelete {
			deletes++
			if r.Path != "/api/clients/alice" {
				t.Fatalf("expected DELETE /api/clients/alice, got %s", r.Path)
			}
		}
	}
	if deletes != 1 {
		t.Fatalf("expected exactly one DELETE call, got %d", deletes)
	}

	// The same token must not be redeemable twice.
	res2 := call("delete_client", map[string]any{"name": "alice", "token": token})
	if !res2.IsError {
		t.Fatalf("expected the second delete_client call with a spent token to fail")
	}
	if got := fake.countMethod(http.MethodDelete); got != 1 {
		t.Fatalf("expected still exactly one DELETE call after replay attempt, got %d", got)
	}
}
