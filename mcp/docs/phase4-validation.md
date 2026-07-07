# MCP Server — Phase 4: Live Tunnel Validation

Validation date: 2026-07-07. This is a results record only — no tool code, dashboard code, or `project-context/` content was changed to produce it. All 19 shipped tools (`mcp/docs/tool-surface.md`) were exercised as a real stdio-spawned subprocess against the live, running dashboard, over the already-connected WireGuard tunnel, per the mcp-server route's Phase 4 scope (`project-context/routes/mcp-server/README.md`).

## Environment

- Dashboard address: `172.16.15.1:8080` (compiled-in default; also set explicitly via `MCP_DASHBOARD_ADDR` for this run to confirm the override mechanism works).
- Tunnel interface: `utun6`, `inet 172.16.15.2 --> 172.16.15.2 netmask 0xffffffff`, `UP,POINTOPOINT,RUNNING`.
- Dashboard health at the start of the run: `{"ok":true,"client_store_ready":true}`.
- Live peers before any validation activity (confirmed via `GET /api/clients`): exactly `laptop` (enabled, online) and `test1` (enabled, offline). Both were left untouched throughout — no add/edit/enable/disable/delete call ever targeted either name.
- All mutating validation used one disposable peer, `mcp-phase4-test`, at address `172.16.15.50/32` (verified free — the only other peer addresses in use were `172.16.15.2/32` and `172.16.15.3/32`) with a throwaway keypair generated via `wg genkey | wg pubkey` (public key `eD+yG3eDEqgGiUY7wgCPbH77vocazFskQ0UM+4gt4hY=`; private key discarded, peer never actually connects).

## Harness

A throwaway Go MCP client (`phase4harness`, its own `go.mod` module, `github.com/modelcontextprotocol/go-sdk v1.6.1`) lives entirely in the session scratchpad (not in this repo). It:

- Built the real `mcp-server` binary (`go build ./cmd/mcp-server`) and spawned it via `mcp.CommandTransport{Command: exec.Command(bin)}` with `MCP_DASHBOARD_ADDR=172.16.15.1:8080` set on the child process's environment — the true stdio-subprocess path (`mcp.NewClient(...).Connect(ctx, transport, nil)`), not an in-process call.
- Called `ClientSession.ListTools` then `ClientSession.CallTool` for every tool, exactly as an MCP host would.
- Cross-checked tool output against a direct `http.Get`/`curl` of the same dashboard endpoint for `list_clients`, `get_health`, `get_server_info`, and `get_client_metrics`.
- Ran the full confirm-gate and delete-token-flow sequences against `mcp-phase4-test` only, with an `/api/health` check after every mutating call.
- Kept the same subprocess alive across the entire run (single `main()` invocation, ~7 minutes wall clock including the real 305s token-expiry sleep) so the delete-token expiry test exercised a genuinely aged, never-restarted in-memory `Store`.

The API surface used (`go doc github.com/modelcontextprotocol/go-sdk/mcp`) was confirmed before writing any harness code, not guessed: `mcp.NewClient`, `Client.Connect`, `CommandTransport`, `ClientSession.CallTool`/`ListTools`, `CallToolParams{Name, Arguments}`, `CallToolResult.{Content, IsError}`.

## Per-tool results (all 19)

| Tool | Result | Evidence |
|---|---|---|
| `get_metrics` | PASS | 285,822-byte combined system+traffic series returned, `isError=false`. |
| `get_system_metrics` | PASS | 176,218-byte series, `range:"24h"` default applied. |
| `get_traffic_metrics` | PASS | 109,590-byte series, `range:"24h"` default applied. |
| `get_client_metrics` | **FAIL** | `dashboard returned HTTP 404 for /api/metrics/client/OYR4niUZ%2FAy5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4=: 404 page not found`. See Discrepancies below — this is a real wrapper bug, not a flaky call. |
| `list_clients` | PASS | 480-byte body, identity fields (name/address/public_key/enabled) matched a same-moment direct `curl` exactly. |
| `get_client_config` | PASS | Returned a valid `wg-quick` config block for `name=laptop` (`[Interface]`/`[Peer]` stanzas, real server public key). |
| `get_client_history` | PASS | 367-byte JSON history for `name=laptop`, `range:"24h"` default. |
| `get_service_status` | PASS | `{"status":{"active":true,"state":"active","active_enter_timestamp":"2026-07-03T10:51:41Z"},...}`. |
| `get_server_info` | PASS | `{"public_ip":"3.216.7.94","port":51820,"server_public_key":"S4Om5BhIcS8gW5VSsr1PrvcSqnPz4EXE7FbhjdqTTQc="}`, byte-identical to direct `curl`. |
| `get_alerts` | PASS | `{"enabled":false,"active":[],"recent":[]}`. |
| `get_snapshot` | PASS | 948–950-byte fan-out body containing server/service/clients sections. |
| `get_geo` | PASS | `{"peers":[{"name":"laptop","lat":48.3452,"lon":33.4974,"city":"Zhovti Vody","country":"UA",...}],"not_mappable":0}`. |
| `get_health` | PASS | `{"ok":true,"client_store_ready":true}`, byte-identical to direct `curl`. |
| `add_client` | PASS | See confirm-gate section below. |
| `edit_client` | PASS | See confirm-gate section below. |
| `enable_client` | PASS | See confirm-gate section below. |
| `disable_client` | PASS | See confirm-gate section below. |
| `preview_delete_client` | PASS | See delete token-flow section below. |
| `delete_client` | PASS | See delete token-flow section below. |

18/19 tools passed. 1 tool (`get_client_metrics`) failed on every invocation due to a reproducible bug (below), not a transient tunnel issue — confirmed by an independent `curl` reproduction outside the harness.

### Cross-checks against direct `curl`

- `list_clients` vs `GET /api/clients`: stable identity fields (`name`, `address`, `public_key`, `enabled`) matched exactly. Byte-for-byte equality was **not** expected and did not hold, because `transfer_rx`/`transfer_tx` are live counters that tick up between the tool call and the following `curl` call even a few hundred milliseconds later (observed directly: two `curl` calls 1s apart on an otherwise idle tunnel differed only in those two counter fields). This is expected live-system behavior, not a wrapper defect.
- `get_health` vs `GET /api/health`: byte-identical.
- `get_server_info` vs `GET /api/server`: byte-identical.
- `get_client_metrics` vs `GET /api/metrics/client/{pubkey}`: **did not match** — the tool call 404'd while the direct endpoint returned a 80KB+ time series. Root cause below.

## Confirm-gate mutating pass (`mcp-phase4-test` only)

All of the following ran against the disposable peer; `laptop`/`test1` were never targeted.

| Step | Result | Evidence |
|---|---|---|
| `add_client` with `confirm` omitted | **Rejected, zero side effect** | Rejected at the go-sdk's JSON-schema layer, before the tool handler ran at all: `validating "arguments": validating root: required: missing properties: ["confirm"]`. `list_clients` confirmed no peer was created. |
| `add_client` with explicit `confirm: false` (supplemental check) | **Rejected, zero side effect** | This time reached the Go handler (schema validation passes — `false` satisfies "property present"), and `requireConfirm` rejected it: `this is a mutating operation and requires confirm=true; re-invoke the same tool call with confirm=true to execute it (no request was sent to the dashboard)`. Confirms both the SDK-level and handler-level gates work correctly and independently. |
| `add_client` with `confirm: true` | **Success** | Peer created at `172.16.15.50/32`; `list_clients` showed it immediately after. Tunnel health checked immediately after: `{"ok":true,...}` — no drop. |
| `disable_client` confirm omitted | **Rejected**, peer stayed enabled | — |
| `disable_client` confirm=true | **Success** | `enabled` flipped to `false` in `list_clients`. Health OK after. |
| `enable_client` confirm=true | **Success** | `enabled` flipped back to `true`. Health OK after. |
| `edit_client` confirm omitted | **Rejected**, note stayed unset | — |
| `edit_client` confirm=true (`note: "phase4 validation"`) | **Success** | `note` field updated and visible in the next `list_clients` read. Health OK after. |

Every mutating call in this pass was followed by an `/api/health` check; all returned HTTP 200 with `client_store_ready:true` — no tunnel drop, no instance replacement, consistent with the Clients & Connectivity route's "live apply via `wg-sync`, no tunnel drop" invariant.

## Delete token-flow pass (`mcp-phase4-test` only)

| Step | Result | Evidence |
|---|---|---|
| `preview_delete_client("mcp-phase4-test")` — token A | **PASS** | Returned accurate current state (`public_key`, `status: offline`, `enabled: true`) plus a token; peer confirmed still present after (preview mutates nothing). |
| `delete_client(name, "deadbeefwrongtoken")` | **Rejected** | `token is invalid, expired, or already used; call preview_delete_client(...) again`. Peer still present. |
| `delete_client(name, "")` | **Rejected** | `token is required; call preview_delete_client(name="mcp-phase4-test") first`. Peer still present. |
| **Real 5-minute expiry wait** | Slept 2026-07-07T15:52:22+03:00 → 2026-07-07T15:57:27+03:00 (305s), same process, same subprocess, token A never redeemed in between. | — |
| `delete_client(name, tokenA)` after the wait | **Rejected as expired** | Same "invalid, expired, or already used" message. Peer still present — token A's 5-minute TTL held under a genuine, non-mocked wait. |
| `preview_delete_client("mcp-phase4-test")` — token B | **PASS** | Fresh token issued. |
| `delete_client(name, tokenB)` | **Success** | `mcp-phase4-test` removed. `list_clients` confirmed it gone immediately after; only `laptop` and `test1` remained. Health check OK immediately after. |

## Wrapper-only and subprocess-hygiene findings

- **Wrapper-only, confirmed by code inspection**: `grep -rn "sqlite" mcp/ --include="*.go"` → no hits. `grep -rn "exec.Command" mcp/ --include="*.go"` → no hits. `grep -rn "wg show\|wg-quick\|/usr/bin/wg" mcp/ --include="*.go"` → two hits, both in tool `Description` strings (`tools.go:90`, `tools.go:94`) describing what the *dashboard* does under the hood — not an invocation. The only outward-facing calls anywhere in `mcp/` are `internal/dashboard.Client`'s `http.Client` requests. This mechanically confirms the mcp-server route's "wrapper, never touches SQLite or `wg` directly" invariant.
- **Subprocess hygiene**: after `ClientSession.Close()`, `pgrep -fl <built-binary-path>` found no lingering process, both after the read-only-only smoke run and after the full mutating+delete run. No Docker container, no always-on listener was used anywhere in this validation — stdio only, matching the route's transport invariant.
- `go build ./...` and `go vet ./...` in `mcp/` are clean (unchanged from before this validation — no tool code was touched). `go test ./...` passes (`wireguard-mcp/internal/tools` ok, cached).

## Discrepancies / failures

**One real bug found: `get_client_metrics` double-percent-encodes the pubkey path segment, producing a 404 on every call.**

- `tools.go`'s `get_client_metrics` handler builds `path := "/api/metrics/client/" + url.PathEscape(in.Pubkey)` — this already turns `/` into `%2F` (correctly, per the handler's own doc comment explaining why `PathEscape` and not `QueryEscape` is used).
- That already-escaped string is then passed into `dashboard.Client.do()`, which builds `url.URL{Path: path, ...}` and calls `u.String()`. `url.URL.Path` is documented to hold the **decoded** path — `String()`/`EscapedPath()` re-escapes it. Because the incoming string already contains a literal `%` character (from the first `PathEscape` pass), that `%` itself gets escaped again: `%2F` → `%252F`.
- Reproduced directly and independently of the harness:
  - `curl http://172.16.15.1:8080/api/metrics/client/OYR4niUZ%2FAy5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4=` → HTTP 200, real time-series body (confirms the dashboard endpoint itself is fine).
  - `curl http://172.16.15.1:8080/api/metrics/client/OYR4niUZ%252FAy5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4=` (the actual bytes the wrapper sends, confirmed via a standalone `net/url` repro: `url.URL{Path: "/api/metrics/client/" + url.PathEscape(pubkey)}.String()` literally produces `%252F` in the output) → HTTP 404 `page not found`.
- Every public key that base64-encodes to contain a `/` character will trigger this (a large fraction of real WireGuard keys, since base64 has a 1/64 chance per non-padding character and keys are 43 characters long). Keys without a `/` (or other char requiring path-escaping) would not exhibit this bug, which is presumably why it wasn't caught by the existing httptest-based unit tests if those don't happen to use a `/`-containing pubkey fixture.
- **This is out of scope to fix under this task** (validation only, no tool-code changes permitted). Flagging for a follow-up fix: `dashboard.Client.do` should either accept a pre-escaped path segment via `url.URL.Opaque`/manual string concatenation instead of the `Path` field, or `get_client_metrics` should stop pre-escaping and let `Client.do` do the escaping itself (passing the raw pubkey and escaping once, in one place). No other tool is affected — `get_client_config` and `get_client_history` path-escape a client *name* (typically has no `/`), and this specific double-escape only bit `get_client_metrics` because its input (a base64 pubkey) is the one identifier likely to contain `/`.

No other discrepancies. All 18 remaining tools, the full confirm-gate pass, and the full delete-token-flow pass (including the real 305-second expiry wait) behaved exactly as designed.

## End-state confirmation

Final `GET /api/clients` after the run:

```json
[{"name":"laptop","address":"172.16.15.2/32","public_key":"OYR4niUZ/Ay5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4=", ...,"enabled":true},
 {"name":"test1","address":"172.16.15.3/32","public_key":"EPu9o5OntZuqmQwVIP0QLJWxh3GTI8KGi6qKzaC1czU=", ...,"enabled":true}]
```

Exactly `laptop` and `test1`, both enabled — the live peer set was restored to its pre-validation state. `mcp-phase4-test` was fully removed via the successful `delete_client(token B)` call above (no manual `curl -X DELETE` cleanup was needed). Final `/api/health`: `{"ok":true,"client_store_ready":true}`.
