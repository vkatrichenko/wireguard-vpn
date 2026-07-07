package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// pathRecordingDashboard is an httptest-backed stand-in that records the raw
// escaped path of every request it receives, so tests can assert the exact
// bytes the dashboard.Client put on the wire (not just the Go-decoded
// r.URL.Path, which would hide a %2F -> %252F double-encoding regression).
type pathRecordingDashboard struct {
	mu            sync.Mutex
	escapedPaths  []string
	decodedPaths  []string
	lastRawTarget string
}

func newPathRecordingDashboard() (*pathRecordingDashboard, *dashboard.Client, func()) {
	f := &pathRecordingDashboard{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.escapedPaths = append(f.escapedPaths, r.URL.EscapedPath())
		f.decodedPaths = append(f.decodedPaths, r.URL.Path)
		f.lastRawTarget = r.RequestURI
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	return f, dashboard.New(addr), srv.Close
}

func (f *pathRecordingDashboard) last() (escaped, decoded string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.escapedPaths)
	if n == 0 {
		return "", ""
	}
	return f.escapedPaths[n-1], f.decodedPaths[n-1]
}

// newReadOnlyTestServer wires the real Register (the entry point every
// read-only tool, including get_client_metrics/get_client_config/
// get_client_history, goes through in production) onto an in-memory MCP
// client/server pair.
func newReadOnlyTestServer(t *testing.T, client *dashboard.Client) func(name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.0"}, nil)
	Register(server, client)

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

// TestGetClientMetricsSendsSinglyEncodedPubkey is the end-to-end regression
// guard for the double-percent-encoding bug (see internal/dashboard/
// client_test.go for the lower-level unit guard). get_client_metrics'
// handler already calls url.PathEscape(pubkey) exactly once (tools.go); this
// test proves dashboard.Client.do() no longer re-escapes that on top,
// against a pubkey containing both "/" and "+".
func TestGetClientMetricsSendsSinglyEncodedPubkey(t *testing.T) {
	const rawPubkey = "OYR4niUZ/Ay5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4="

	fake, client, closeSrv := newPathRecordingDashboard()
	defer closeSrv()
	call := newReadOnlyTestServer(t, client)

	res := call("get_client_metrics", map[string]any{"pubkey": rawPubkey})
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	escaped, decoded := fake.last()
	wantDecoded := "/api/metrics/client/" + rawPubkey
	if decoded != wantDecoded {
		t.Fatalf("decoded request path = %q, want %q", decoded, wantDecoded)
	}
	if !strings.Contains(escaped, "%2F") {
		t.Fatalf("escaped request path = %q, want it to contain a singly-encoded %%2F", escaped)
	}
	if strings.Contains(escaped, "%252F") {
		t.Fatalf("escaped request path = %q, contains %%252F: pubkey's %%2F was double-encoded", escaped)
	}
}

// TestGetClientConfigSendsSinglyEncodedName proves the same do() fix covers
// the name-keyed tools too, not just the pubkey-keyed one.
func TestGetClientConfigSendsSinglyEncodedName(t *testing.T) {
	const rawName = "office/laptop"

	fake, client, closeSrv := newPathRecordingDashboard()
	defer closeSrv()
	call := newReadOnlyTestServer(t, client)

	res := call("get_client_config", map[string]any{"name": rawName})
	if res.IsError {
		t.Fatalf("unexpected error result: %+v", res)
	}

	escaped, decoded := fake.last()
	wantDecoded := "/api/clients/" + rawName + "/config"
	if decoded != wantDecoded {
		t.Fatalf("decoded request path = %q, want %q", decoded, wantDecoded)
	}
	if !strings.Contains(escaped, "%2F") {
		t.Fatalf("escaped request path = %q, want it to contain a singly-encoded %%2F", escaped)
	}
	if strings.Contains(escaped, "%252F") {
		t.Fatalf("escaped request path = %q, contains %%252F: name's %%2F was double-encoded", escaped)
	}
}
