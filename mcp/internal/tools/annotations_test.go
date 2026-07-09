package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestToolAnnotations is Praxis Task #12's verification: every tool
// registered by this package (both Register's read-only set and
// RegisterMutating's mutating set) must carry a ToolAnnotations hint
// matching its read-only/destructive/idempotent classification in
// mcp/docs/tool-surface.md. This lists tools the same way a real MCP client
// would (ListTools over the wire), not by inspecting server internals.
func TestToolAnnotations(t *testing.T) {
	ctx := context.Background()

	_, client, closeSrv := newFakeDashboard("[]")
	defer closeSrv()

	server := mcp.NewServer(&mcp.Implementation{Name: "test-server", Version: "0.0.0"}, nil)
	Register(server, client)
	RegisterMutating(server, client, NewStore())

	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, t1, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}

	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := mcpClient.Connect(ctx, t2, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}

	byName := make(map[string]*mcp.Tool, len(res.Tools))
	for _, tl := range res.Tools {
		byName[tl.Name] = tl
	}

	// Every tool must be present and every tool must carry non-nil
	// Annotations — a tool silently missing hints would be a regression this
	// test should catch, not just the sampled assertions below.
	wantNames := []string{
		"get_metrics", "get_system_metrics", "get_traffic_metrics", "get_client_metrics",
		"list_clients", "get_client_config", "get_client_history", "get_service_status",
		"get_server_info", "get_alerts", "get_snapshot", "get_geo", "get_health", "get_host_metrics",
		"add_client", "edit_client", "enable_client", "disable_client",
		"preview_delete_client", "delete_client",
	}
	if len(wantNames) != 20 {
		t.Fatalf("test itself is wrong: want 20 tool names, got %d", len(wantNames))
	}
	if len(res.Tools) != 20 {
		t.Fatalf("ListTools returned %d tools, want 20 (count must not change)", len(res.Tools))
	}
	for _, name := range wantNames {
		tl, ok := byName[name]
		if !ok {
			t.Errorf("tool %q missing from ListTools result", name)
			continue
		}
		if tl.Annotations == nil {
			t.Errorf("tool %q has nil Annotations", name)
		}
	}

	// Representative sample, per class.
	cases := []struct {
		name            string
		wantReadOnly    bool
		wantDestructive *bool // nil means "don't assert"
	}{
		{"get_health", true, nil},
		{"get_client_metrics", true, nil},
		{"get_host_metrics", true, nil},
		{"preview_delete_client", true, nil},
		{"add_client", false, boolPtr(false)},
		{"edit_client", false, boolPtr(false)},
		{"enable_client", false, boolPtr(false)},
		{"disable_client", false, boolPtr(false)},
		{"delete_client", false, boolPtr(true)},
	}
	for _, c := range cases {
		tl, ok := byName[c.name]
		if !ok {
			t.Errorf("%s: not found in ListTools result", c.name)
			continue
		}
		ann := tl.Annotations
		if ann == nil {
			t.Errorf("%s: Annotations is nil", c.name)
			continue
		}
		if ann.ReadOnlyHint != c.wantReadOnly {
			t.Errorf("%s: ReadOnlyHint = %v, want %v", c.name, ann.ReadOnlyHint, c.wantReadOnly)
		}
		if c.wantDestructive != nil {
			if ann.DestructiveHint == nil {
				t.Errorf("%s: DestructiveHint = nil, want %v", c.name, *c.wantDestructive)
			} else if *ann.DestructiveHint != *c.wantDestructive {
				t.Errorf("%s: DestructiveHint = %v, want %v", c.name, *ann.DestructiveHint, *c.wantDestructive)
			}
		}
	}

	// enable_client/disable_client/edit_client are the tools this task calls
	// out as genuinely idempotent; add_client must NOT be.
	idempotentTrue := []string{"enable_client", "disable_client", "edit_client"}
	for _, name := range idempotentTrue {
		if !byName[name].Annotations.IdempotentHint {
			t.Errorf("%s: IdempotentHint = false, want true", name)
		}
	}
	if byName["add_client"].Annotations.IdempotentHint {
		t.Errorf("add_client: IdempotentHint = true, want false")
	}

	// delete_client must be destructive AND not read-only.
	del := byName["delete_client"].Annotations
	if del.ReadOnlyHint {
		t.Errorf("delete_client: ReadOnlyHint = true, want false")
	}
	if del.DestructiveHint == nil || !*del.DestructiveHint {
		t.Errorf("delete_client: DestructiveHint = %v, want true", del.DestructiveHint)
	}
}
