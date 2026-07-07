// Package dashboard is the only package in this module that speaks HTTP to
// the wireguard-dashboard. Every tool handler in internal/tools calls through
// Client — this keeps the wrapper-only invariant (mcp-server route,
// project-context/routes/mcp-server/README.md) mechanically true: there is
// exactly one place a request could be built wrong, and it never touches
// anything but the dashboard's already-public /api/* surface.
package dashboard

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds each dashboard call. The dashboard's own handlers
// (including the /api/snapshot fan-out across every backend service) return
// in milliseconds under normal operation, so 10s is deliberately generous
// headroom rather than a tuned value — a hung tunnel should fail loudly, not
// hang the calling LLM turn indefinitely.
const DefaultTimeout = 10 * time.Second

// maxBodySnippet caps how much of a non-2xx response body gets echoed back in
// error text, so a misbehaving endpoint returning a large HTML error page
// doesn't blow up the tool-call error message.
const maxBodySnippet = 512

// Client is a thin wrapper around http.Client scoped to one dashboard base
// address. One instance is constructed at startup in cmd/mcp-server/main.go
// and shared by every tool handler — there is no per-call or per-tool state
// to isolate, unlike the dashboard's own proc.Service/processes.Service
// singletons which hold prior-sample state under a mutex.
type Client struct {
	// BaseAddr is host:port only (no scheme) — the dashboard is only ever
	// reachable over plain HTTP on the WireGuard tunnel (no app-layer TLS,
	// no auth; see the mcp-server route's "Auth model" section).
	BaseAddr string
	HTTP     *http.Client
}

// New builds a Client targeting addr.
func New(addr string) *Client {
	return &Client{
		BaseAddr: addr,
		HTTP:     &http.Client{Timeout: DefaultTimeout},
	}
}

// StatusError reports a non-2xx HTTP response from the dashboard. Tool
// handlers propagate this unwrapped (the go-sdk's ToolHandlerFor packs any
// returned error into CallToolResult.Content with IsError set) so the calling
// LLM sees the exact status code and a body snippet instead of a generic
// "request failed".
type StatusError struct {
	Path       string
	StatusCode int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("dashboard returned HTTP %d for %s: %s", e.StatusCode, e.Path, e.Body)
}

// Get issues GET http://<BaseAddr><path>?<query> and returns the raw response
// body. Deliberately untyped: this is a pure wrapper (see mcp/README.md) that
// never re-models the dashboard's JSON response shapes, so the body is
// proxied through as-is for the tool handler to embed as text content.
func (c *Client) Get(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := url.URL{
		Scheme:   "http",
		Host:     c.BaseAddr,
		Path:     path,
		RawQuery: query.Encode(),
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", path, err)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		// Connection refused, DNS failure, and timeouts all land here. On a
		// laptop-side MCP wrapper (per the mcp-server route) the single most
		// likely cause is that the operator's WireGuard tunnel to this
		// project isn't connected — say so explicitly so the calling LLM (or
		// the operator reading its output) doesn't have to guess at a bare
		// net/http dial error.
		return nil, fmt.Errorf("could not reach dashboard at %s%s: %w (is the WireGuard tunnel to this project connected?)", c.BaseAddr, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body from %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > maxBodySnippet {
			snippet = snippet[:maxBodySnippet] + "…"
		}
		return nil, &StatusError{Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(snippet)}
	}

	return body, nil
}
