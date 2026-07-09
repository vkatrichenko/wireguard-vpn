// Package tools registers the dashboard-wrapping MCP tools. Each read-only
// tool in this file is a thin GET wrapper around one dashboard /api/*
// endpoint — see mcp/docs/tool-surface.md for the endpoint-to-tool mapping
// this mirrors. list_clients, get_client_config, and get_client_history are
// read-only but were deliberately held back from the Phase 2 batch and ship
// here alongside Phase 3's mutating tools (internal/tools/mutating.go) so the
// entire /api/clients* surface lands as one reviewable unit — see
// mcp/docs/tool-surface.md and project-context/routes/mcp-server/README.md.
//
// Handlers never re-model the dashboard's JSON response shapes: the raw body
// is proxied through as the tool's text content (see internal/dashboard.Get).
// This keeps the wrapper decoupled from every endpoint's response schema, per
// the mcp-server route's "wrapper, not new business logic" framing.
package tools

import (
	"context"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// rangeArgs is the input shape for tools whose dashboard endpoint accepts an
// optional ?range= query param. Range is passed through verbatim — this
// wrapper never validates the dashboard's own range grammar (e.g. "1h",
// "24h"); that validation belongs to the dashboard handler, not the wrapper.
type rangeArgs struct {
	Range string `json:"range,omitempty" jsonschema:"optional time range window (e.g. '1h', '24h'), passed through verbatim to the dashboard"`
}

// noArgs is used by tools whose endpoint takes no query parameters.
type noArgs struct{}

// clientMetricsArgs is get_client_metrics' input. Pubkey is required because
// that endpoint is keyed by WireGuard public key, not client name (unlike
// get_client_config/get_client_history below, which are name-keyed).
type clientMetricsArgs struct {
	Pubkey string `json:"pubkey" jsonschema:"the WireGuard public key identifying the client, as returned by list_clients or the dashboard UI"`
	Range  string `json:"range,omitempty" jsonschema:"optional time range window (e.g. '1h', '24h'), passed through verbatim to the dashboard"`
}

// clientNameArgs is get_client_config's input: just the peer name used as the
// {name} path segment.
type clientNameArgs struct {
	Name string `json:"name" jsonschema:"the client's name, as returned by list_clients"`
}

// clientNameRangeArgs is get_client_history's input: the peer name plus the
// same optional ?range= passthrough as the metrics tools above.
type clientNameRangeArgs struct {
	Name  string `json:"name" jsonschema:"the client's name, as returned by list_clients"`
	Range string `json:"range,omitempty" jsonschema:"optional time range window (e.g. '1h', '24h', '7d'), passed through verbatim to the dashboard"`
}

// Register adds every Phase 2 read-only tool to server, wired against client.
// Called once from cmd/mcp-server/main.go after both are constructed.
func Register(server *mcp.Server, client *dashboard.Client) {
	addRangeTool(server, client, "get_metrics", "/api/metrics",
		"Combined system+traffic time-series feed powering the dashboard's trend charts.")
	addRangeTool(server, client, "get_system_metrics", "/api/metrics/system",
		"Host system-metrics time-series (CPU, memory, etc.) for a given range.")
	addRangeTool(server, client, "get_traffic_metrics", "/api/metrics/traffic",
		"wg0 cumulative traffic (rx/tx) time-series for a given range.")

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_client_metrics",
		Description: "Per-client rx/tx rate time-series, keyed by WireGuard public key.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clientMetricsArgs) (*mcp.CallToolResult, any, error) {
		if in.Pubkey == "" {
			return nil, nil, fmt.Errorf("pubkey is required")
		}
		q := url.Values{}
		if in.Range != "" {
			q.Set("range", in.Range)
		}
		// url.PathEscape, not url.QueryEscape: pubkeys are base64 and can
		// contain "+" and "/", which QueryEscape would turn into "%2B"/"%2F"
		// in a way that's correct for a query string but wrong for a path
		// segment's percent-encoding rules (e.g. "/" must become %2F either
		// way, but PathEscape is the semantically correct escaper here since
		// this is a path segment, not a query value).
		path := "/api/metrics/client/" + url.PathEscape(in.Pubkey)
		return get(ctx, client, path, q)
	})

	addNoArgTool(server, client, "list_clients", "/api/clients",
		"Joined peer list: manifest metadata (name, address, note, enabled) plus live `wg show wg0 dump` state (status, handshake, byte counters, endpoint, geo) per client.")

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_client_config",
		Description: "Downloadable wg-quick config text for one client, keyed by name.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clientNameArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		path := "/api/clients/" + url.PathEscape(in.Name) + "/config"
		return get(ctx, client, path, nil)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "get_client_history",
		Description: "Per-client connection-history summary (sessions, online/offline, last-seen) over an optional ?range= window, keyed by name.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clientNameRangeArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		q := url.Values{}
		if in.Range != "" {
			q.Set("range", in.Range)
		}
		path := "/api/clients/" + url.PathEscape(in.Name) + "/history"
		return get(ctx, client, path, q)
	})

	addNoArgTool(server, client, "get_service_status", "/api/service",
		"WireGuard service health: running/stopped, last-start time, derived uptime.")
	addNoArgTool(server, client, "get_server_info", "/api/server",
		"Server identity/endpoint facts: public IP, listening port, server public key, build metadata.")
	addNoArgTool(server, client, "get_alerts", "/api/alerts",
		"Current in-UI alert state.")
	addNoArgTool(server, client, "get_snapshot", "/api/snapshot",
		"Fan-out snapshot across all backend services in parallel - a single \"everything at once\" read.")
	addNoArgTool(server, client, "get_geo", "/api/geo",
		"Mappable-peer snapshot (GeoIP-resolved endpoints) for the geo map.")
	addNoArgTool(server, client, "get_health", "/api/health",
		"Liveness/readiness probe, including client_store_ready. Call this first to sanity-check the MCP-to-dashboard round trip over the WireGuard tunnel before calling other tools.")

	// get_host_metrics is the one exception to this file's "every tool wraps
	// one /api/* endpoint" doc comment above — it wraps the sibling
	// Prometheus /metrics endpoint instead (Task #11; see host_metrics.go).
	// Registered here, not in host_metrics.go, so Register stays the single
	// place every read-only tool this server exposes gets listed.
	addHostMetricsTool(server, client)
}

// addRangeTool registers a param-less-except-range tool: GET path with an
// optional ?range= passthrough. Every caller of this helper is read-only, so
// the ToolAnnotations are hardcoded here rather than threaded through as a
// parameter — see readOnlyAnnotations (annotations.go).
func addRangeTool(server *mcp.Server, client *dashboard.Client, name, path, desc string) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: desc, Annotations: readOnlyAnnotations()},
		func(ctx context.Context, _ *mcp.CallToolRequest, in rangeArgs) (*mcp.CallToolResult, any, error) {
			q := url.Values{}
			if in.Range != "" {
				q.Set("range", in.Range)
			}
			return get(ctx, client, path, q)
		})
}

// addNoArgTool registers a tool with no input arguments at all: a bare GET
// path. Every caller of this helper is read-only, so the ToolAnnotations are
// hardcoded here — see readOnlyAnnotations (annotations.go).
func addNoArgTool(server *mcp.Server, client *dashboard.Client, name, path, desc string) {
	mcp.AddTool(server, &mcp.Tool{Name: name, Description: desc, Annotations: readOnlyAnnotations()},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
			return get(ctx, client, path, nil)
		})
}

// get performs the GET and, on success, wraps the raw JSON body as the tool's
// text content. The Out type is `any` deliberately (see AddTool's doc: an Out
// of `any` omits the output schema and skips auto-populating Content) so we
// build CallToolResult by hand with the untouched response body — this is the
// "proxy the raw JSON through" design called for in the task brief, not an
// oversight.
//
// On failure the error is returned as-is: ToolHandlerFor packs any returned
// error into CallToolResult.Content with IsError set, so the calling LLM sees
// the dashboard.StatusError / connection-refused message verbatim instead of
// a generic failure.
func get(ctx context.Context, client *dashboard.Client, path string, query url.Values) (*mcp.CallToolResult, any, error) {
	body, err := client.Get(ctx, path, query)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
	}, nil, nil
}
