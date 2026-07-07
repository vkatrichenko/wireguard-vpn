# wireguard-mcp

A stdio [Model Context Protocol](https://modelcontextprotocol.io) server that lets an LLM agent (Claude
Code, or any other MCP host) manage WireGuard peers and read live metrics, status, and health by wrapping
the dashboard's `/api/*` REST API. It must be able to reach the dashboard **over the WireGuard tunnel**
(the same `172.16.15.1:8080` the UI and `wg-peer` use) — it adds no new privileges beyond that API, and no
new access path beyond the tunnel.

---

## Requirements

- A reachable dashboard over the tunnel — the same one the web UI and `wg-peer` already use.
- The `MCP_DASHBOARD_ADDR` env var, set to that dashboard's `host:port`. (The compiled-in default is
  `172.16.15.1:8080` — this project's own tunnel bind address — so it's often not strictly required, but set
  it explicitly if you're retargeting the binary or running against a dev instance.)

**Auth model, stated honestly:** the dashboard has no application-layer authentication — WireGuard tunnel
membership is the entire access boundary, and this MCP server inherits that and adds none. The
confirmation gates below (see "Safety gates") are a **client-side safety mechanism against LLM
over-eagerness**, not a security boundary. Anyone who can reach the dashboard over the tunnel already has
full access by design.

---

## Install

Two paths — pick one; full details (Go-toolchain version, Gatekeeper quarantine, checksum/signature
verification) are in the canonical reference, [`../mcp/README.md`](../mcp/README.md).

**Path 1 — `go install` (primary):** installs a binary named **`mcp-server`**.

```sh
go install github.com/vkatrichenko/wireguard-vpn/mcp/cmd/mcp-server@v0.0.3
```

Requires a Go toolchain matching `mcp/go.mod`. The version selector is the plain semver (`@v0.0.3`) — the
underlying git tag is `mcp/v0.0.3`, but Go maps the selector onto it internally; `@mcp/v0.0.3` fails.

**Path 2 — prebuilt release binary:** for machines without a Go toolchain. This produces a binary named
**`wireguard-mcp`** (a different name from Path 1's `mcp-server`, same program). Two ways to do it.

**2a — download the binary (no checksum step).** Grab the archive for your OS/arch — copy its download URL
from the [releases page](https://github.com/vkatrichenko/wireguard-vpn/releases) (the example below is
macOS arm64), then:

```sh
curl -LO https://github.com/vkatrichenko/wireguard-vpn/releases/download/mcp/v0.0.3/wireguard-mcp_0.0.3_darwin_arm64.tar.gz
tar xzf wireguard-mcp_0.0.3_darwin_arm64.tar.gz
xattr -d com.apple.quarantine ./wireguard-mcp      # macOS only
sudo mv wireguard-mcp /usr/local/bin/
claude mcp add wireguard-vpn --env MCP_DASHBOARD_ADDR=<ip>:8080 -- /usr/local/bin/wireguard-mcp
```

Swap the archive name for your platform (`linux_amd64`, `linux_arm64`, `darwin_amd64`, `darwin_arm64`, or
the `windows_amd64.zip`) and replace `<ip>:8080` with your dashboard's tunnel address. No checksum step is
required; an optional SHA-256 + cosign verification is documented in [`../mcp/README.md`](../mcp/README.md).

**2b — one-line install script (verifies under the hood).**

```sh
curl -fsSL https://raw.githubusercontent.com/vkatrichenko/wireguard-vpn/main/mcp/install.sh | sh
```

The script detects your OS/arch, downloads the matching asset + `checksums.txt`, verifies the SHA-256
**silently**, extracts, installs to `/usr/local/bin`, and prints the `claude mcp add` line for you to run.
Zero manual verification, but integrity is still checked under the hood — it refuses to install on a
checksum mismatch. (As with any `curl … | sh` installer, you're trusting the fetched script; read it first
at [`mcp/install.sh`](../mcp/install.sh) if you'd rather.)

---

## Register with your MCP host

Point your MCP host (Claude Code, Claude Desktop, etc.) at whichever binary you installed. The binary name
differs by install path, so make sure the `command` below matches what you actually have.

**If installed via `go install`** (binary `mcp-server`, resolved via `PATH`):

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

**If downloaded as a release binary** (binary `wireguard-mcp`, referenced by absolute path):

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

Precedence for the dashboard target is `-addr` flag > `MCP_DASHBOARD_ADDR` env > the compiled-in default
(`172.16.15.1:8080`) — only one override is needed in practice.

---

## Tool surface

19 tools total, one per dashboard endpoint. Source of truth: [`../mcp/docs/tool-surface.md`](../mcp/docs/tool-surface.md).

**Read-only (13):**

`get_metrics`, `get_system_metrics`, `get_traffic_metrics`, `get_client_metrics`, `list_clients`,
`get_client_config`, `get_client_history`, `get_service_status`, `get_server_info`, `get_alerts`,
`get_snapshot`, `get_geo`, `get_health`

**Mutating (6):**

`add_client`, `edit_client`, `enable_client`, `disable_client`, `preview_delete_client`, `delete_client`

---

## Safety gates

Mutating tools are gated by reversibility, not treated uniformly — full mechanics in
[`../mcp/docs/confirmation-gates.md`](../mcp/docs/confirmation-gates.md).

- **`add_client`, `edit_client`, `enable_client`, `disable_client`** — trivially reversible, so each takes
  an inline `confirm` argument. Without `confirm=true`, the handler returns an error **before any HTTP
  request is sent**; with `confirm=true`, it executes immediately in the same call.
- **`delete_client`** — the sole irreversible verb on this surface, so it's gated harder with a two-step,
  token-based dry run:
  1. `preview_delete_client(name)` — read-only; shows the peer's current state and issues a short-lived,
     single-use token.
  2. `delete_client(name, token)` — redeems that token and only then calls the delete. A missing, wrong, or
     expired token means no delete request is sent.

These gates protect against a wrong inference by the agent (misread intent, stale premise, accidental
retry) — they are explicitly **not** an authentication or authorization layer.

---

## See also

- [`../mcp/README.md`](../mcp/README.md) — the canonical install/build/dev reference for this module.
- [`project-context/routes/mcp-server/README.md`](../project-context/routes/mcp-server/README.md) — the
  route/mission document this module implements.
