# wireguard-mcp

A stdio [Model Context Protocol](https://modelcontextprotocol.io) server that lets an LLM agent read
the `wireguard-dashboard`'s live status (metrics, service health, alerts, geo, snapshot) **and** manage
WireGuard peers (add/edit/enable/disable/delete) over the WireGuard tunnel. This module is the completed
implementation of the mcp-server route (`project-context/routes/mcp-server/README.md`) — all five phases
(tool-surface design, read-only tools, mutating tools, live-tunnel validation, and this wiring/packaging
pass) are done.

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

## Tool surface — 19 tools

Every tool is a thin wrapper around exactly one dashboard `/api/*` endpoint (full design rationale,
endpoint corrections, and the one-tool-per-endpoint decision are in `docs/tool-surface.md` — treat that
file as the source of truth; the tables below mirror it).

### Read-only (13)

| Tool | Endpoint |
|---|---|
| `get_metrics` | `GET /api/metrics` |
| `get_system_metrics` | `GET /api/metrics/system` |
| `get_traffic_metrics` | `GET /api/metrics/traffic` |
| `get_client_metrics` | `GET /api/metrics/client/{pubkey}` |
| `list_clients` | `GET /api/clients` |
| `get_client_config` | `GET /api/clients/{name}/config` |
| `get_client_history` | `GET /api/clients/{name}/history` |
| `get_service_status` | `GET /api/service` |
| `get_server_info` | `GET /api/server` |
| `get_alerts` | `GET /api/alerts` |
| `get_snapshot` | `GET /api/snapshot` |
| `get_geo` | `GET /api/geo` |
| `get_health` | `GET /api/health` |

### Mutating (6)

| Tool | Endpoint | Gate |
|---|---|---|
| `add_client` | `POST /api/clients` | inline `confirm=true` |
| `edit_client` | `PATCH /api/clients/{name}` | inline `confirm=true` |
| `enable_client` | `PATCH /api/clients/{name}` (`enabled=true`) | inline `confirm=true` |
| `disable_client` | `PATCH /api/clients/{name}` (`enabled=false`) | inline `confirm=true` |
| `preview_delete_client` | `GET /api/clients` (read-only lookup) | none — issues the token `delete_client` needs |
| `delete_client` | `DELETE /api/clients/{name}` | single-use, 5-minute token from a prior `preview_delete_client` call |

`add_client`/`edit_client`/`enable_client`/`disable_client` reject the call (no HTTP request sent) unless
`confirm=true` is passed explicitly — these four are trivially reversible operations. `delete_client` is
the sole irreversible verb on this surface, so it's gated harder: call `preview_delete_client(name)` first
to see the peer's current state and receive a token, then `delete_client(name, token)` to redeem it. Full
mechanics (token TTL, single-use, most-recent-wins, constant-time compare, why the split by reversibility)
are in `docs/confirmation-gates.md` — read it before relying on or changing this behavior.

The only thing deliberately absent from this tool surface is **application-layer auth**: the dashboard has
none today (WireGuard tunnel membership is the entire perimeter), and this wrapper inherits that and adds
none, per the mcp-server route's explicit, owner-accepted risk acceptance.

## How it works

Read-only tool handlers build a `GET` request against `http://<addr><path>`, forward an optional `range`
query param verbatim, and return the dashboard's raw JSON response body as the tool's text content
(`internal/tools/tools.go`'s `get` helper). Mutating tool handlers do the same for `POST`/`PATCH`/`DELETE`
(`internal/tools/mutating.go`). Response bodies are **never re-modeled or re-typed** — this keeps the
wrapper decoupled from every endpoint's JSON shape, so a dashboard response-shape change never requires a
matching MCP-side change. All HTTP logic lives in one place, `internal/dashboard/client.go`, so there's
exactly one code path that could get request-building wrong.

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
exist to override for local dev (e.g. pointing at `make run`'s `127.0.0.1:8080`) or to retarget the same
binary at a different project's tunnel (see "Cross-project adaptation" below), not to support multi-target
selection at runtime.

## Building

```sh
cd mcp
go build -o wireguard-mcp ./cmd/mcp-server
```

No cross-compilation flags are required — unlike the dashboard, this binary runs on the operator's own
laptop (whatever OS/arch that is), not on the EC2 host, so there's no `CGO_ENABLED=0 GOOS=linux
GOARCH=amd64` constraint here.

## Installation

Tagged releases (`mcp/vX.Y.Z`) are built and published by `.github/workflows/mcp-release.yml` via
GoReleaser, producing signed, checksummed binaries for `linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`, and `windows/amd64`. Two install paths are supported; pick one.

**A note on binary names**: the two paths below produce a binary with a **different name**. `go install`
always names the binary after its package directory (`cmd/mcp-server` → `mcp-server`). The GoReleaser
release build (and the `make`-style local `go build -o wireguard-mcp ./cmd/mcp-server` above) explicitly
names it `wireguard-mcp` instead. Same binary, two names, depending on how you got it — make sure your
`mcpServers` `command` matches whichever one you actually installed.

### Path 1: `go install` (primary)

Requires only a Go toolchain — no download, no checksum verification step, works identically on every
OS/arch Go itself supports:

```sh
go install github.com/vkatrichenko/wireguard-vpn/mcp/cmd/mcp-server@mcp/v0.1.0
```

The `mcp/v0.1.0` version suffix (not just `v0.1.0`) is required because this module lives in the `mcp/`
subdirectory of the repo, not at its root — Go's module-in-subdirectory convention tags the module path
prefix onto the version. Always pin an exact tag here; never `@latest` or `@mcp` (floating branch), so a
`go install` from one machine reproduces the same binary as another.

The installed binary lands in `$GOBIN` if set, otherwise `$(go env GOPATH)/bin` — and per the naming note
above, it is named **`mcp-server`**, not `wireguard-mcp`.

### Path 2: prebuilt release binary

Download the archive matching your OS/arch from
[GitHub Releases](https://github.com/vkatrichenko/wireguard-vpn/releases), then verify it against the
release's `checksums.txt` before running anything:

```sh
shasum -a 256 -c checksums.txt --ignore-missing
```

On macOS, Gatekeeper quarantines anything downloaded via a browser or `curl` from a non-notarized source;
clear it before running the extracted binary:

```sh
xattr -d com.apple.quarantine wireguard-mcp
```

(A Homebrew tap would strip the quarantine attribute automatically as part of `brew install` — Homebrew
distribution is deliberately deferred for this project; see "GoReleaser and future distribution" below.)
The extracted binary is named **`wireguard-mcp`** (per the naming note above), matching the `Building`
section's local `go build -o wireguard-mcp` convention.

Each release's checksums are additionally signed keylessly (Sigstore/cosign, OIDC — no key management) as
part of `mcp-release.yml`; this is exercised for real only in CI on a tagged run, not in local snapshot
builds.

### GoReleaser and future distribution

The release config is `mcp/.goreleaser.yaml`. A Homebrew tap (GoReleaser's `brews:` block) would remove the
manual quarantine-clearing step above and is a natural next step, but is explicitly out of scope for now —
add it later as its own change, not bundled into this packaging pass.

## Manual invocation (example MCP host config)

Point an MCP host (Claude Code, Claude Desktop, etc.) at the installed or downloaded binary. Both override
forms are shown below; precedence is `-addr` flag > `MCP_DASHBOARD_ADDR` env > the compiled-in default
(`172.16.15.1:8080`), so only one is needed in practice — showing both here for reference.

**If installed via `go install`** (binary named `mcp-server`, on `PATH` if `$GOBIN`/`$(go env GOPATH)/bin`
is on it):

```json
{
  "mcpServers": {
    "wireguard-vpn": {
      "command": "mcp-server",
      "env": {
        "MCP_DASHBOARD_ADDR": "172.16.15.1:8080"
      }
    }
  }
}
```

**If downloaded as a release binary** (named `wireguard-mcp`, referenced by absolute path — no `PATH`
assumption):

```json
{
  "mcpServers": {
    "wireguard-vpn": {
      "command": "/absolute/path/to/wireguard-mcp",
      "args": ["-addr", "172.16.15.1:8080"]
    }
  }
}
```

Since `172.16.15.1:8080` is already the compiled-in default, neither override is strictly necessary for
this project — they're shown here as the pattern to copy when retargeting the same binary elsewhere (see
below).

## Cross-project adaptation (repeatable template)

The owner runs several unrelated VPN servers for different projects, one MCP server per project, never one
server multiplexing several. The same `wireguard-mcp` binary is the template for all of them — retargeting
it at a different project's dashboard requires **no recompile and no code change**, only:

1. **The dashboard target** — set `MCP_DASHBOARD_ADDR` (or pass `-addr`) to that project's own WireGuard
   tunnel `IP:port`. The `172.16.15.1:8080` compiled into this binary is only this project's convenience
   default; it's never load-bearing for a different project's config.
2. **The `mcpServers` key name** — rename `"wireguard-vpn"` to whatever identifies the other project (e.g.
   `"other-project-vpn"`) so the MCP host's tool list and the operator's own mental model don't conflate
   the two servers.

Everything else stays identical across every project this pattern is applied to: stdio transport, no
Docker, the full 19-tool set (all of `docs/tool-surface.md`), the wrapper-only architecture (no SQLite/`wg`
access, dashboard `/api/*` is the only thing ever called), and no application-layer auth (each project's
dashboard would need to carry the same "WireGuard tunnel membership is the perimeter" posture for this to
be an acceptable fit). If a target project's dashboard doesn't expose the same `/api/*` surface, the tool
set itself would need porting — this template only covers same-shaped dashboards.

## Validation status

Live-validated in Phase 4 (2026-07-07) as a real stdio-spawned subprocess against the real, running
dashboard over the connected WireGuard tunnel — not mocked, not in-process. Initial result: 18/19 tools
passing; the one failure (`get_client_metrics` double-percent-encoding a pubkey path segment, causing a 404
on any key containing `/`) was root-caused and fixed the same day (Task #6, in `internal/dashboard/client.go`'s
`do()`). All 19 tools now pass, confirmed by live re-validation against a real `/`-containing pubkey.
Full results, evidence, and the confirm-gate / delete-token-flow validation (including a real, non-mocked
305-second token-expiry wait) are recorded in `docs/phase4-validation.md`.

## Package layout

- `cmd/mcp-server/main.go` — resolves the dashboard address (flag → env → default), constructs the MCP
  server and dashboard client, registers both the read-only and mutating tool sets, runs over stdio, and
  handles SIGINT/SIGTERM via a cancellable context (mirroring `dashboard/cmd/wireguard-dashboard/main.go`'s
  own idiom).
- `internal/dashboard/client.go` — the only HTTP client in this module; owns request-building,
  timeouts, and error classification (`StatusError` for non-2xx, a tunnel-aware message for connection
  failures).
- `internal/tools/tools.go` — registers the 13 read-only tools against the dashboard client. Handlers are
  thin: build query params, call the client, wrap the raw body as text content.
- `internal/tools/mutating.go` — registers the 6 mutating `/api/clients*` tools (`add_client`,
  `edit_client`, `enable_client`, `disable_client`, `preview_delete_client`, `delete_client`) and their
  confirm/token gating logic.
- `internal/tools/tokens.go` — the in-memory, per-process `Store` backing `delete_client`'s token gate
  (issue, verify, single-use, 5-minute TTL, constant-time compare).
- `docs/tool-surface.md` — the design document mapping every dashboard endpoint (read-only and mutating)
  onto a tool name; this README's tool-surface section mirrors it.
- `docs/confirmation-gates.md` — the resolved design behind the mutating tools' confirm/token gates.
- `docs/phase4-validation.md` — the live-tunnel validation record (all 19 tools, confirm-gate pass,
  delete-token-flow pass, the `get_client_metrics` bug and its fix).
