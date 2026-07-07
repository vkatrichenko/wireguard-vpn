# wireguard-mcp

A stdio [Model Context Protocol](https://modelcontextprotocol.io) server that lets an LLM agent read the
`wireguard-dashboard`'s live status (metrics, service health, alerts, geo, snapshot) over the WireGuard
tunnel. This is **Phase 2** of the mcp-server route (`project-context/routes/mcp-server/README.md`):
scaffold + read-only tools only, to validate the MCP-to-dashboard round trip with zero mutation risk
before Phase 3 adds any peer-CRUD tool.

It is a separate Go module (`wireguard-mcp`) from the dashboard, deliberately never embedded in the
dashboard binary and never deployed to the EC2 instance — see the mcp-server route's Invariants. It runs
laptop-side, spawned on-demand by an MCP host, and reaches the dashboard exactly like any other WireGuard
tunnel client.

## Why this SDK

The official `github.com/modelcontextprotocol/go-sdk`, pinned exactly at `v1.6.1`, was chosen over a
third-party or hand-rolled MCP implementation because:

- It's maintained by Anthropic in collaboration with Google (the two biggest MCP-consuming ecosystems),
  so it tracks the spec rather than lagging it.
- It's Go, matching this repo's `dashboard/` stack exactly — no new language, no new toolchain, no CGO
  concerns (the dashboard's hard `CGO_ENABLED=0` constraint doesn't even apply here since this module
  never touches SQLite, but staying std-lib-first and single-binary is consistent with the rest of the
  repo).
- It compiles to one static binary, matching this repo's "single static binary" deployment philosophy even
  though this particular binary runs on the operator's laptop, not on EC2.
- Stdio transport is built into the SDK (`mcp.StdioTransport`) — no extra framing/transport code to write
  or maintain, which matters for a solo-maintained, soon-to-be-open-sourced repo.

## Scope: what Phase 2 does and does not include

Per the mcp-server route's roadmap ("Phase 2 — scaffold and ship read-only tools only
(metrics/status/service/server/alerts/snapshot/geo)"), this module implements **exactly** these ten tools,
each a thin GET wrapper around one dashboard endpoint (full mapping in `docs/tool-surface.md`):

| Tool | Endpoint |
|---|---|
| `get_metrics` | `GET /api/metrics` |
| `get_system_metrics` | `GET /api/metrics/system` |
| `get_traffic_metrics` | `GET /api/metrics/traffic` |
| `get_client_metrics` | `GET /api/metrics/client/{pubkey}` |
| `get_service_status` | `GET /api/service` |
| `get_server_info` | `GET /api/server` |
| `get_alerts` | `GET /api/alerts` |
| `get_snapshot` | `GET /api/snapshot` |
| `get_geo` | `GET /api/geo` |
| `get_health` | `GET /api/health` |

`get_health` is included as the connectivity sanity-check tool: Phase 2's stated purpose is validating the
MCP-to-dashboard round trip, and a liveness probe is the lowest-risk way to confirm the tunnel and target
are reachable before calling anything else. `docs/tool-surface.md` flagged `/api/health` as "pending owner
sign-off on scope" since it isn't named in the mcp-server route's own endpoint list (though it is
registered in the dashboard's mux) — this implementation includes it; if the owner later decides it should
be excluded, removing it is a one-line change (delete the `addNoArgTool(... "get_health" ...)` call in
`internal/tools/tools.go`).

### Deliberately NOT implemented (not even behind a flag)

- **All mutating tools** — `add_client` (POST), `update_client` (PATCH), `delete_client` (DELETE). These
  are Phase 3, gated on the still-open confirmation-gate question (inline `confirm` param vs. a separate
  dry-run tool) that this module does not resolve.
- **`list_clients`, `get_client_config`, `get_client_history`** — despite being read-only endpoints,
  these are deliberately deferred to Phase 3 rather than shipped here. `docs/tool-surface.md` (Phase 1)
  optimistically listed them under "Phase 2," but the owner-approved mcp-server route (line 30) scopes
  Phase 2 to "metrics/status/service/server/alerts/snapshot/geo" only — it does not mention `/api/clients*`
  at all under Phase 2. Rather than re-litigate that scoping in code, this implementation follows the
  route text literally: all of `/api/clients*` (the three read-only endpoints above, plus the three
  mutating ones) ships together in Phase 3, so the entire client-management surface lands in one
  reviewable unit instead of being split across two phases for no functional benefit.
- **Application-layer auth.** The dashboard has none today (WireGuard tunnel membership is the entire
  perimeter); this wrapper inherits that and adds none, per the route's explicit, owner-accepted risk
  acceptance.

## How it works

Every tool handler does exactly one thing: build a `GET` request against `http://<addr><path>`, forward an
optional `range` query param verbatim, and return the dashboard's raw JSON response body as the tool's text
content (`internal/tools/tools.go`'s `get` helper). Response bodies are **never re-modeled or re-typed** —
this keeps the wrapper decoupled from every endpoint's JSON shape, so a dashboard response-shape change
never requires a matching MCP-side change. All HTTP logic lives in one place, `internal/dashboard/client.go`,
so there's exactly one code path that could get request-building wrong.

On a non-2xx response, the tool call fails with a `dashboard.StatusError` naming the status code and a body
snippet. On a connection failure (refused, timeout, DNS), the error message explicitly asks "is the
WireGuard tunnel to this project connected?" — the single most likely cause for a laptop-side wrapper that
can only ever reach `172.16.15.1:8080` while tunneled in.

## Configuration

| Knob | Default | Purpose |
|---|---|---|
| `-addr` flag | (unset) | Highest-precedence override of the dashboard target. |
| `MCP_DASHBOARD_ADDR` env | (unset) | Falls back to this if `-addr` isn't passed. |
| compiled-in default | `172.16.15.1:8080` | The dashboard's own production WireGuard tunnel bind address. |

Per the mcp-server route, one MCP server instance addresses exactly one hardcoded target — these knobs
exist to override for local dev (e.g. pointing at `make run`'s `127.0.0.1:8080`), not to support
multi-target selection at runtime.

## Building

```sh
cd mcp
go build -o wireguard-mcp ./cmd/mcp-server
```

No cross-compilation flags are required — unlike the dashboard, this binary runs on the operator's own
laptop (whatever OS/arch that is), not on the EC2 host, so there's no `CGO_ENABLED=0 GOOS=linux
GOARCH=amd64` constraint here.

## Manual invocation (example MCP host config)

```json
{
  "mcpServers": {
    "wireguard-vpn": {
      "command": "/absolute/path/to/wireguard-mcp/wireguard-mcp",
      "env": {
        "MCP_DASHBOARD_ADDR": "172.16.15.1:8080"
      }
    }
  }
}
```

**This has NOT been validated against the real production dashboard over the actual WireGuard tunnel.**
Everything above was verified by building, vetting, and smoke-testing the binary in isolation (it starts
cleanly, logs to stderr only so stdout stays a clean JSON-RPC channel, and shuts down on SIGINT/SIGTERM) —
none of it was exercised against a live dashboard instance or a real MCP host. Live validation against the
real tunnel and dashboard, checked against the Clients & Connectivity route's invariants, is Phase 4 of the
mcp-server route, not this phase.

## Package layout

- `cmd/mcp-server/main.go` — resolves the dashboard address (flag → env → default), constructs the MCP
  server and dashboard client, registers tools, runs over stdio, and handles SIGINT/SIGTERM via a
  cancellable context (mirroring `dashboard/cmd/wireguard-dashboard/main.go`'s own idiom).
- `internal/dashboard/client.go` — the only HTTP client in this module; owns request-building,
  timeouts, and error classification (`StatusError` for non-2xx, a tunnel-aware message for connection
  failures).
- `internal/tools/tools.go` — registers the ten Phase 2 tools against the dashboard client. Handlers are
  thin: build query params, call the client, wrap the raw body as text content.
- `docs/tool-surface.md` — the Phase 1 design document mapping every dashboard endpoint (read-only and
  mutating) onto a tool name; this README's scope section restates only the Phase 2 subset actually
  implemented here.
