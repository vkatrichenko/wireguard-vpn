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
