// Phase 3 mutating peer-CRUD tools, extending the Phase 2 read-only surface
// in tools.go. The confirmation-gate design was resolved by the owner on
// 2026-07-06 (see mcp/docs/confirmation-gates.md) and is NOT re-litigated
// here:
//
//   - add_client, edit_client, enable_client, disable_client each take an
//     explicit `confirm bool` arg. confirm != true (missing or false) is
//     rejected before any HTTP call is made — no request reaches the
//     dashboard, so a hesitant or exploratory call from the calling LLM
//     never mutates anything. confirm == true executes immediately in that
//     same call; there is no token or second round-trip for these four.
//   - delete_client is the sole irreversible /api/clients* operation (a
//     removed peer's keypair and history are gone for good, where every
//     other verb here is trivially reversible with another call), so it gets
//     a harder two-tool gate: preview_delete_client(name) issues a
//     short-lived, single-use token (see tokens.go) alongside a
//     human-readable preview; delete_client(name, token) redeems that token
//     and only then calls DELETE.
//
// Every handler here is still a thin wrapper: the dashboard's
// /api/clients* handlers (dashboard/internal/server/handlers_clients_admin.go
// and handlers_clients.go) remain the sole validation and mutation
// authority. A rejected add/edit (bad name, duplicate key, address out of
// subnet, etc.) surfaces to the calling LLM as the dashboard's own
// dashboard.StatusError body — this package never duplicates that
// validation.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vkatrichenko/wireguard-vpn/mcp/internal/dashboard"
)

// addClientArgs is add_client's input, matching POST /api/clients' JSON body
// exactly (parseClientAdd in handlers_clients_admin.go): name, public_key,
// optional address (empty → the dashboard auto-allocates one), optional
// note.
type addClientArgs struct {
	Name      string `json:"name" jsonschema:"unique peer name"`
	PublicKey string `json:"public_key" jsonschema:"the peer's WireGuard public key (base64)"`
	Address   string `json:"address,omitempty" jsonschema:"optional tunnel address override; leave empty to auto-allocate"`
	Note      string `json:"note,omitempty" jsonschema:"optional free-text note"`
	Confirm   bool   `json:"confirm" jsonschema:"must be true to execute; false or omitted rejects the call before contacting the dashboard"`
}

// editClientArgs is edit_client's input. Name targets the peer to edit (used
// as the PATCH path parameter, {name} in PATCH /api/clients/{name}); NewName
// maps onto the PATCH body's own "name" field for a rename, mirroring
// parseClientUpdate's present-vs-absent PATCH semantics
// (handlers_clients_admin.go). enabled toggling deliberately has no field
// here — that is enable_client/disable_client's job, per the task's tool
// split, not a third way to flip the same bit.
//
// Limitation (documented, not a bug): because every field below is a flat
// string rather than a pointer, this wrapper cannot distinguish "leave
// unchanged" from "explicitly set to empty" — an empty NewName/PublicKey/
// Address/Note is always treated as "omit this field from the PATCH body,"
// so this tool cannot be used to explicitly blank out an existing note or
// address. That is an acceptable trade for a flat MCP tool schema; clearing
// a field this way is left to the dashboard UI directly.
type editClientArgs struct {
	Name      string `json:"name" jsonschema:"the current name of the peer to edit"`
	NewName   string `json:"new_name,omitempty" jsonschema:"optional new name to rename the peer to; omit to leave the name unchanged"`
	PublicKey string `json:"public_key,omitempty" jsonschema:"optional replacement WireGuard public key; omit to leave unchanged"`
	Address   string `json:"address,omitempty" jsonschema:"optional replacement tunnel address; omit to leave unchanged"`
	Note      string `json:"note,omitempty" jsonschema:"optional replacement free-text note; omit to leave unchanged"`
	Confirm   bool   `json:"confirm" jsonschema:"must be true to execute; false or omitted rejects the call before contacting the dashboard"`
}

// clientToggleArgs is enable_client and disable_client's shared input shape
// — both are a name plus the confirm gate; which boolean gets PATCHed is
// fixed by which tool was called, not by an argument.
type clientToggleArgs struct {
	Name    string `json:"name" jsonschema:"the peer name to toggle"`
	Confirm bool   `json:"confirm" jsonschema:"must be true to execute; false or omitted rejects the call before contacting the dashboard"`
}

// previewDeleteArgs is preview_delete_client's input: just the peer name.
// There is no confirm gate here — this tool is read-only by construction (it
// never calls DELETE), so the only thing an over-eager call can do is issue
// a token that expires unused in 5 minutes.
type previewDeleteArgs struct {
	Name string `json:"name" jsonschema:"the peer name to preview deleting"`
}

// deleteClientArgs is delete_client's input: the peer name plus the token
// issued by a prior preview_delete_client call for that exact name.
type deleteClientArgs struct {
	Name  string `json:"name" jsonschema:"the peer name to delete; must match the name passed to preview_delete_client"`
	Token string `json:"token" jsonschema:"the token returned by preview_delete_client for this name"`
}

// clientRowPreview is the subset of GET /api/clients' ClientRow
// (dashboard/internal/server/clientrows.go) that preview_delete_client
// renders. Fields are intentionally plain strings/bool here (not time.Time)
// so a missing/zero value degrades to an empty string we can render as
// "unknown" rather than requiring a time-parse that could itself fail.
type clientRowPreview struct {
	Name            string `json:"name"`
	PublicKey       string `json:"public_key"`
	Status          string `json:"status"`
	LatestHandshake string `json:"latest_handshake,omitempty"`
	Enabled         bool   `json:"enabled"`
}

// RegisterMutating adds every Phase 3 mutating tool to server, wired against
// client and store. Called once from cmd/mcp-server/main.go alongside
// Register (the Phase 2 read-only tools), after both client and store are
// constructed.
func RegisterMutating(server *mcp.Server, client *dashboard.Client, store *Store) {
	mcp.AddTool(server, &mcp.Tool{
		Name:        "add_client",
		Description: "Add a new WireGuard peer. MUTATING: requires confirm=true, applied live with no tunnel drop (POST /api/clients).",
		// Reversible (delete_client can undo it) but never idempotent: calling
		// it twice with the same args either fails (duplicate name/key) or
		// creates a second peer, never a no-op — so IdempotentHint is left
		// unset/false.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in addClientArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		if in.PublicKey == "" {
			return nil, nil, fmt.Errorf("public_key is required")
		}
		if err := requireConfirm(in.Confirm); err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(struct {
			Name      string `json:"name"`
			PublicKey string `json:"public_key"`
			Address   string `json:"address,omitempty"`
			Note      string `json:"note,omitempty"`
		}{Name: in.Name, PublicKey: in.PublicKey, Address: in.Address, Note: in.Note})
		if err != nil {
			return nil, nil, fmt.Errorf("encoding add_client request body: %w", err)
		}
		return post(ctx, client, "/api/clients", body)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "edit_client",
		Description: "Edit an existing peer's name/public_key/address/note. MUTATING: requires confirm=true (PATCH /api/clients/{name}). Use enable_client/disable_client to toggle enabled state.",
		// Re-applying the same edit (same target fields, same values) PATCHes
		// the peer to the identical state again, so IdempotentHint is true.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in editClientArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		if err := requireConfirm(in.Confirm); err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(struct {
			Name      *string `json:"name,omitempty"`
			PublicKey *string `json:"public_key,omitempty"`
			Address   *string `json:"address,omitempty"`
			Note      *string `json:"note,omitempty"`
		}{
			Name:      nonEmptyPtr(in.NewName),
			PublicKey: nonEmptyPtr(in.PublicKey),
			Address:   nonEmptyPtr(in.Address),
			Note:      nonEmptyPtr(in.Note),
		})
		if err != nil {
			return nil, nil, fmt.Errorf("encoding edit_client request body: %w", err)
		}
		return patch(ctx, client, "/api/clients/"+url.PathEscape(in.Name), body)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "enable_client",
		Description: "Enable a peer. MUTATING: requires confirm=true (PATCH /api/clients/{name} with enabled=true).",
		// Re-enabling an already-enabled peer PATCHes enabled=true onto a peer
		// already enabled=true — a genuine no-op, so IdempotentHint is true.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clientToggleArgs) (*mcp.CallToolResult, any, error) {
		return toggleClient(ctx, client, in, true)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "disable_client",
		Description: "Disable a peer. MUTATING: requires confirm=true (PATCH /api/clients/{name} with enabled=false).",
		// Same reasoning as enable_client above, mirrored: re-disabling an
		// already-disabled peer is a no-op.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in clientToggleArgs) (*mcp.CallToolResult, any, error) {
		return toggleClient(ctx, client, in, false)
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "preview_delete_client",
		Description: "Read-only dry run before delete_client: shows the named peer's current state and issues a single-use, 5-minute token required by delete_client. Never mutates anything.",
		Annotations: readOnlyAnnotations(),
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in previewDeleteArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}

		row, err := fetchClientRow(ctx, client, in.Name)
		if err != nil {
			return nil, nil, err
		}
		if row == nil {
			// No token issued for a peer that doesn't exist — see the
			// package doc comment: preview must never hand out a redeemable
			// token for a delete that can't possibly succeed.
			return nil, nil, fmt.Errorf("no client named %q (no token issued)", in.Name)
		}

		token, err := store.Issue(in.Name)
		if err != nil {
			return nil, nil, err
		}

		pubkey := row.PublicKey
		if pubkey == "" {
			pubkey = "unknown"
		}
		lastSeen := row.LatestHandshake
		if lastSeen == "" {
			lastSeen = "unknown"
		}
		status := row.Status
		if status == "" {
			status = "unknown"
		}

		text := fmt.Sprintf(
			"Preview delete for %q:\n  public_key: %s\n  status: %s\n  enabled: %t\n  last_handshake: %s\n\nTo proceed, call delete_client(name=%q, token=%q). This token expires in %s and is single-use.",
			in.Name, pubkey, status, row.Enabled, lastSeen, in.Name, token, tokenTTL,
		)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil, nil
	})

	mcp.AddTool(server, &mcp.Tool{
		Name:        "delete_client",
		Description: "Delete a peer by name. Irreversible. Requires a token from a prior preview_delete_client call for the same name (DELETE /api/clients/{name}).",
		// The sole irreversible verb on this surface (package doc comment
		// above) — DestructiveHint true, ReadOnlyHint false.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true)},
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in deleteClientArgs) (*mcp.CallToolResult, any, error) {
		if in.Name == "" {
			return nil, nil, fmt.Errorf("name is required")
		}
		if in.Token == "" {
			return nil, nil, fmt.Errorf("token is required; call preview_delete_client(name=%q) first", in.Name)
		}
		if !store.Verify(in.Name, in.Token) {
			return nil, nil, fmt.Errorf("token is invalid, expired, or already used; call preview_delete_client(name=%q) again to get a fresh token", in.Name)
		}
		return del(ctx, client, "/api/clients/"+url.PathEscape(in.Name))
	})
}

// toggleClient is enable_client and disable_client's shared body: both are
// confirm-gated PATCHes that set exactly one field, {"enabled": enabled}.
func toggleClient(ctx context.Context, client *dashboard.Client, in clientToggleArgs, enabled bool) (*mcp.CallToolResult, any, error) {
	if in.Name == "" {
		return nil, nil, fmt.Errorf("name is required")
	}
	if err := requireConfirm(in.Confirm); err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(struct {
		Enabled bool `json:"enabled"`
	}{Enabled: enabled})
	if err != nil {
		return nil, nil, fmt.Errorf("encoding enabled-toggle request body: %w", err)
	}
	return patch(ctx, client, "/api/clients/"+url.PathEscape(in.Name), body)
}

// requireConfirm is the shared confirm-gate check for add_client, edit_client,
// enable_client, and disable_client. Returning an error here — before any
// call into internal/dashboard — is what makes confirm!=true a true no-op:
// the caller sees an actionable rejection and the dashboard never receives a
// request.
func requireConfirm(confirm bool) error {
	if !confirm {
		return fmt.Errorf("this is a mutating operation and requires confirm=true; re-invoke the same tool call with confirm=true to execute it (no request was sent to the dashboard)")
	}
	return nil
}

// nonEmptyPtr returns nil for an empty string, or a pointer to s otherwise.
// Used to build edit_client's PATCH body: a nil pointer with `omitempty`
// drops the JSON key entirely, matching parseClientUpdate's present-vs-absent
// PATCH semantics (handlers_clients_admin.go) for the "leave unchanged" case.
func nonEmptyPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// fetchClientRow calls GET /api/clients and returns the row matching name, or
// nil (not an error) if no such peer exists. Used only by
// preview_delete_client — every other mutating tool acts directly on
// {name} without a pre-fetch, since the dashboard itself is the source of
// truth for whether that name exists (a 404 from PATCH/DELETE already
// reports that).
func fetchClientRow(ctx context.Context, client *dashboard.Client, name string) (*clientRowPreview, error) {
	body, err := client.Get(ctx, "/api/clients", nil)
	if err != nil {
		return nil, err
	}
	var rows []clientRowPreview
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decoding GET /api/clients response: %w", err)
	}
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// post, patch, and del mirror tools.go's get helper: proxy the dashboard's
// raw response body through as the tool's text content on success, or
// propagate the error (dashboard.StatusError / dial failure) unwrapped on
// failure so the calling LLM sees the dashboard's exact rejection reason.
func post(ctx context.Context, client *dashboard.Client, path string, body []byte) (*mcp.CallToolResult, any, error) {
	respBody, err := client.Post(ctx, path, body)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(respBody)}}}, nil, nil
}

func patch(ctx context.Context, client *dashboard.Client, path string, body []byte) (*mcp.CallToolResult, any, error) {
	respBody, err := client.Patch(ctx, path, body)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(respBody)}}}, nil, nil
}

func del(ctx context.Context, client *dashboard.Client, path string) (*mcp.CallToolResult, any, error) {
	respBody, err := client.Delete(ctx, path)
	if err != nil {
		return nil, nil, err
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(respBody)}}}, nil, nil
}
