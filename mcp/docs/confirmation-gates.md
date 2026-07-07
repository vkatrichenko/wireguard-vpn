# MCP Server — Phase 3: Confirmation-Gate Design

This documents the resolved design behind the mutating `/api/clients*` tools shipped in Phase 3 (`mcp/internal/tools/mutating.go`), so Phase 4 (live-tunnel validation) and future readers don't need to re-derive it from code. The design itself was settled by the owner on 2026-07-06 — this document records it, it does not re-open it (see `mcp/docs/tool-surface.md`'s "Open question (deferred to Phase 3)" section for the question as it stood before this resolution).

## Why any gate at all

The dashboard has no application-layer auth (see `project-context/routes/mcp-server/README.md`'s "Auth model") — the WireGuard tunnel membership boundary is the only access control, and that's an owner-accepted, settled risk. The confirmation gates below are a **client-side safety mechanism against LLM over-eagerness, not a security boundary.** An LLM agent operating this wrapper can misread intent, retry an already-successful call, or act on a stale premise; the gates exist to put a hard stop between "the model decided to call a mutating tool" and "the dashboard actually mutated state," so a wrong inference costs a rejected call instead of a real peer change. They do not, and are not intended to, protect against a malicious caller — anyone who can reach the dashboard over the tunnel already has full access by design.

## Two gate shapes, chosen by reversibility

`add_client`, `edit_client`, `enable_client`, and `disable_client` are all trivially reversible: adding a peer can be undone by deleting it, editing a field can be undone by editing it back, and enable/disable is a single-bit toggle in either direction. `delete_client` is the one operation on this surface that is **not** reversible — a removed peer's keypair, address allocation, and connection history are gone, and re-adding "the same" peer with a fresh keypair is not the same peer. That asymmetry is why the two families use different gate shapes:

### 1. Inline `confirm` parameter (add/edit/enable/disable)

Each of these four tools takes a `confirm bool` argument.

- `confirm` missing or `false` → the handler returns an error **before any HTTP call is made**. The dashboard never sees a request; nothing is mutated. The error text tells the caller to re-invoke the same tool call with `confirm=true`.
- `confirm == true` → the handler executes immediately, in that same call. There is no token, no second round-trip, no separate "confirm" tool — the caller (typically an LLM that has already decided, in a single turn, that the mutation is correct) sets the flag and the call goes straight through.

This is deliberately a single-call design: for a reversible operation, the cost of a wrong call is low enough that a second network round-trip (a token flow) buys little extra safety and adds latency/complexity for no real benefit.

### 2. Two-tool, token-gated dry run (delete only)

`delete_client` is hardened harder because it's the sole irreversible verb:

1. **`preview_delete_client(name)`** — read-only. It fetches the peer's current state via `GET /api/clients` (the same endpoint `list_clients`/the dashboard UI already use) and renders a human-readable preview: name, public key, status, enabled state, last handshake. It then calls `Store.Issue(name)` to mint a fresh token and returns it embedded in the preview text. If `name` doesn't match any known peer, an error is returned and **no token is issued** — there is no way to obtain a redeemable token for a peer that doesn't exist. This tool never calls `DELETE` and cannot mutate anything, by construction (it only ever calls `client.Get`).
2. **`delete_client(name, token)`** — calls `Store.Verify(name, token)`. Only on success does it call `DELETE /api/clients/{name}`. A missing, wrong, or expired token produces an error telling the caller to run `preview_delete_client` again; no DELETE request is sent to the dashboard.

## Token semantics (`mcp/internal/tools/tokens.go`)

The token bookkeeping lives in `Store`, constructed once at startup (`cmd/mcp-server/main.go`) and shared by every `preview_delete_client`/`delete_client` call:

- **In-memory, per-process.** Tokens live only in the `mcp-server` process's memory — there is no persistence to disk or any other store. Since the MCP server is a short-lived stdio subprocess spawned fresh per MCP host session (see the mcp-server route), a token not surviving a process restart is the correct behavior, not a gap: there's no "restart and replay an old token" window, because a restarted process has no memory of ever issuing one.
- **Bound to a peer name.** A token issued for `"alice"` only ever redeems a delete of `"alice"`. It is not bound to a public key or any other identity — if the peer is renamed via `edit_client` between preview and delete, the token becomes unredeemable under the old name (and no token exists yet for the new name), which is a safe side effect of the name-keyed design rather than a special case that had to be built.
- **5-minute expiry.** Long enough for an LLM turn (or an operator reading the preview) to review and decide; short enough that a token idling in an old conversation transcript can't be replayed much later against a peer that may have changed underneath it. The constant is `tokenTTL` in `tokens.go`.
- **Single-use.** `Store.Verify` deletes the token entry on a successful match, so the exact same token can never redeem a second `DELETE` call — this is what makes it a one-shot confirmation rather than a standing bearer credential.
- **Most-recent-wins.** Calling `preview_delete_client(name)` again before redeeming the prior token overwrites it: the old token becomes invalid, only the newest one for that name works. There is deliberately no "a token is already pending" error — an LLM agent's natural retry pattern (call preview again if unsure) just works without the caller having to track token lifecycle itself.
- **Constant-time comparison.** `Store.Verify` uses `crypto/subtle.ConstantTimeCompare` rather than `==` for the token match, so a timing side-channel can't help narrow down a guess. This is defense in depth for a value that, unlike a network-facing secret, is only ever compared against input from the same trust boundary (the calling LLM/operator) — cheap to add, not load-bearing for the overall security model.

## What this is not

- **Not an auth layer.** It gates whether *this wrapper* sends a mutating request, given that the caller can already reach the wrapper (and therefore the dashboard, over the tunnel) at all. It says nothing about who is allowed to run the MCP server or connect to the tunnel — that boundary is unchanged and out of scope here, per the mcp-server route's explicit, owner-accepted "no application-layer auth" decision.
- **Not a substitute for the dashboard's own validation.** A confirmed `add_client`/`edit_client` call can still be rejected by the dashboard (duplicate name, invalid public key, address out of subnet, etc.) — that rejection surfaces to the caller as the dashboard's own `dashboard.StatusError` body, unchanged by anything in this document.
