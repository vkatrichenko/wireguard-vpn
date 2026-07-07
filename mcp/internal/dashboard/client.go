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
	return c.do(ctx, http.MethodGet, path, query, nil)
}

// Post issues POST http://<BaseAddr><path> with body as the JSON request
// payload (Content-Type: application/json) and returns the raw response body.
// Used by add_client (POST /api/clients per mcp/docs/tool-surface.md).
func (c *Client) Post(ctx context.Context, path string, body []byte) ([]byte, error) {
	return c.do(ctx, http.MethodPost, path, nil, body)
}

// Patch issues PATCH http://<BaseAddr><path> with body as the JSON request
// payload and returns the raw response body. Used by edit_client,
// enable_client, and disable_client (all PATCH /api/clients/{name} — the
// dashboard's handleUpdateClient treats enabled as just another PATCH-able
// field, per handlers_clients_admin.go).
func (c *Client) Patch(ctx context.Context, path string, body []byte) ([]byte, error) {
	return c.do(ctx, http.MethodPatch, path, nil, body)
}

// Delete issues DELETE http://<BaseAddr><path> with no body and returns the
// raw response body. Used by delete_client (DELETE /api/clients/{name}).
func (c *Client) Delete(ctx context.Context, path string) ([]byte, error) {
	return c.do(ctx, http.MethodDelete, path, nil, nil)
}

// do is the shared request/response plumbing for every HTTP verb this client
// issues. Get, Post, Patch, and Delete are all thin argument-shaping calls
// into this one method so the StatusError mapping, the tunnel-aware dial-
// failure message, and the timeout/base-addr wiring exist in exactly one
// place — matching the package doc's "exactly one place a request could be
// built wrong" invariant. body is the raw JSON payload (nil for a bodyless
// request); a non-nil body sets Content-Type: application/json, matching the
// dashboard's isJSONRequest sniff (handlers_clients_admin.go) so the mutating
// handlers parse via their JSON branch rather than falling through to the
// form-encoded path.
//
// path arrives already percent-encoded by the caller: every tool handler in
// internal/tools that has a dynamic path segment (pubkey or name) calls
// url.PathEscape on that segment exactly once before concatenating it onto a
// literal prefix (see get_client_metrics/get_client_config/get_client_history
// in tools.go). path-escaping is that caller's job, singly, and do() owns
// only "send this path across the wire without touching it again". Do NOT
// route path through url.URL's Path field here — url.URL.Path is the decoded
// form, and u.String()/u.EscapedPath() would re-escape any "%" already in
// path (e.g. turning a correct "%2F" into "%252F", which 404s against the
// dashboard's mux). Building the request from a raw URL string instead lets
// url.Parse (called internally by http.NewRequestWithContext) populate
// RawPath from the already-escaped path we hand it, so EscapedPath() on the
// resulting request reproduces path's encoding verbatim.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body []byte) ([]byte, error) {
	rawURL := "http://" + c.BaseAddr + path
	if encoded := query.Encode(); encoded != "" {
		rawURL += "?" + encoded
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = strings.NewReader(string(body))
	}

	req, err := http.NewRequestWithContext(ctx, method, rawURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body from %s: %w", path, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(respBody)
		if len(snippet) > maxBodySnippet {
			snippet = snippet[:maxBodySnippet] + "…"
		}
		return nil, &StatusError{Path: path, StatusCode: resp.StatusCode, Body: strings.TrimSpace(snippet)}
	}

	return respBody, nil
}
