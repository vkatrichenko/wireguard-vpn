// Praxis Task #12: attach mcp.ToolAnnotations (read-only / destructive /
// idempotent hints) to every tool this package registers. Additive metadata
// only — no tool's arguments, behavior, return bytes, or confirmation-gate
// logic changes here.
//
// mcp.ToolAnnotations has mixed field types (protocol.go): ReadOnlyHint and
// IdempotentHint are plain bool, but DestructiveHint and OpenWorldHint are
// *bool so the wire can distinguish "unset" (client falls back to the
// protocol's stated default) from "explicitly false". boolPtr below is the
// helper for the pointer fields.
//
// OpenWorldHint is left unset (nil) on every tool in this package: every
// tool here talks only to the closed dashboard API over the WireGuard
// tunnel (never an open-ended external system like a web search), so
// "closed world" is stated once here rather than repeated at each call
// site.
package tools

import "github.com/modelcontextprotocol/go-sdk/mcp"

// boolPtr returns a pointer to b, for ToolAnnotations' *bool fields.
func boolPtr(b bool) *bool {
	return &b
}

// readOnlyAnnotations is shared by every read-only tool in this package:
// tools.go's addRangeTool/addNoArgTool helpers (every caller of both is
// read-only), the three direct read-only registrations in tools.go
// (get_client_metrics, get_client_config, get_client_history),
// get_host_metrics (host_metrics.go), and preview_delete_client
// (mutating.go — it only previews and issues a token, it mutates nothing).
func readOnlyAnnotations() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true}
}

// reversibleAnnotations is shared by the reversible mutating tools:
// add_client, edit_client, enable_client, disable_client. All are non-read-only
// and non-destructive — each can be undone by another call on the same
// /api/clients* surface. idempotent is true where re-invoking with the same
// arguments is a genuine no-op (edit_client re-patches identical values;
// enable_client/disable_client re-apply the same enabled state) and false for
// add_client (a second call either fails on the duplicate or creates a second
// peer). delete_client is deliberately NOT covered here — it is the sole
// DestructiveHint tool and keeps its own inline literal.
func reversibleAnnotations(idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: idempotent}
}
