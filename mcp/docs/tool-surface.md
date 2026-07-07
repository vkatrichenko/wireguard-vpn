# MCP Server — Phase 1: Tool-Surface Definition

This is a **planned, no-code design document** — Phase 1 of the owner-approved 5-phase MCP server roadmap (`project-context/routes/mcp-server/README.md`, planning conversation dated 2026-07-06). It maps the dashboard's confirmed `/api/*` endpoints onto discrete, named MCP tools so Phase 2 (scaffold + ship read-only tools) has an unambiguous surface to implement against. No dashboard code, MCP server code, or scaffolding is introduced here.

## Design invariants carried from the mcp-server route

These are settled architectural decisions from the mcp-server route — restated here as context for the tool-surface design, not re-decided:

- **Wrapper-only.** Every MCP tool translates a tool call into an HTTP call against the dashboard's existing `/api/*` endpoints. The wrapper never touches SQLite or `wg` directly — the dashboard remains the sole peer-mutation authority (Clients & Connectivity route, spec 019).
- **Separate external process.** The MCP server is never embedded in the dashboard's Go binary and never deployed to the EC2 instance; it runs laptop-side, reached over the WireGuard tunnel like any other client.
- **Transport is already decided.** MCP's native stdio subprocess, one hardcoded dashboard target (`172.16.15.1:8080`) per instance — this is inherited context from the route, not reconsidered in this document.
- **No application-layer auth.** The dashboard has none today and the MCP wrapper adds none by design. This is a settled, owner-accepted risk (relying on WireGuard tunnel membership) — it is not raised as an open gap in this document.

## Endpoint → tool table

Endpoints below are confirmed against `dashboard/internal/server/server.go`'s mux registration (lines 295–324) against the routes named in the mcp-server route (`/api/clients`, `/api/metrics*`, `/api/service`, `/api/server`, `/api/alerts`, `/api/snapshot`, `/api/geo`). Two corrections and one scope call are noted directly below the table.

| HTTP method + path | Proposed MCP tool name | Read-only / Mutating | One-line purpose |
|---|---|---|---|
| `GET /api/clients` | `list_clients` | Read-only | Joined peer list: manifest metadata + live `wg show wg0 dump` state (handshake, byte counters, endpoint) per client. |
| `POST /api/clients` | `add_client` | Mutating | Add a new peer (name, public key, optional address/note); applied live via `wg-sync`, no tunnel drop. |
| `PATCH /api/clients/{name}` | `update_client` | Mutating | Edit an existing peer's name/public_key/address/note, **and** enable/disable it — `enabled` is just another PATCH-able field, there is no separate enable/disable endpoint. |
| `DELETE /api/clients/{name}` | `delete_client` | Mutating | Remove a peer by name. |
| `GET /api/clients/{name}/config` | `get_client_config` | Read-only | Downloadable `wg-quick` config text for one client (spec 015 migration onto the runtime DB). |
| `GET /api/clients/{name}/history` | `get_client_history` | Read-only | Per-client connection-history summary (sessions, online/offline, last-seen) over a `?range=` window (spec 006). |
| `GET /api/metrics` | `get_metrics` | Read-only | Combined time-series feed powering the trend charts (system + traffic in one response). |
| `GET /api/metrics/system` | `get_system_metrics` | Read-only | Host system-metrics time-series (CPU, memory, etc.) for a given range. |
| `GET /api/metrics/traffic` | `get_traffic_metrics` | Read-only | wg0 cumulative traffic (rx/tx) time-series for a given range. |
| `GET /api/metrics/client/{pubkey}` | `get_client_metrics` | Read-only | Per-client rx/tx rate time-series, keyed by public key (not name). |
| `GET /api/service` | `get_service_status` | Read-only | WireGuard service health: running/stopped, last-start time, derived uptime. |
| `GET /api/server` | `get_server_info` | Read-only | Server identity/endpoint facts: public IP, listening port, server public key, build metadata. |
| `GET /api/alerts` | `get_alerts` | Read-only | Current in-UI alert state. |
| `GET /api/snapshot` | `get_snapshot` | Read-only | Fan-out snapshot across all backend services in parallel — a single "everything at once" read. |
| `GET /api/geo` | `get_geo` | Read-only | Mappable-peer snapshot (GeoIP-resolved endpoints) for the geo map. |
| `GET /api/health` | `get_health` | Read-only | Liveness/readiness probe, including `client_store_ready` (spec 020). Not named in the mcp-server route's endpoint list — see note below. |

### Corrections against the mcp-server route's endpoint list

- **Path-param precision.** The mcp-server route describes the peer-CRUD surface generically as `GET/POST/PATCH/DELETE /api/clients`. The actual mux registration (`server.go:298-303`) only has `GET` and `POST` on the bare `/api/clients` collection; `PATCH` and `DELETE` are scoped to `/api/clients/{name}`. The table above reflects the exact registered paths.
- **No separate enable/disable endpoint.** The route's Core Concepts describe the mutating scope as "add/edit/delete/**enable/disable**" as if enable/disable were a distinct verb. Reading `handlers_clients_admin.go`'s `handleUpdateClient` doc comment confirms `enabled` is just one of the PATCH-able fields on `update_client` (name, public_key, address, note, enabled) — there is no `POST /api/clients/{name}/enable` or similar. One tool (`update_client`) covers both edit and enable/disable; this document does not propose a separate tool for it.
- **`GET /api/health` is unnamed in the route but exists in code.** Registered at `server.go:295`, ahead of every other `/api/*` entry, and it is a natural low-risk Phase 2 candidate (it is how the wrapper would sanity-check tunnel connectivity to the dashboard before calling anything else). Included above as `get_health`; flagging its absence from the route's endpoint list as a gap for the owner to bless in Phase 2, not a routes/code contradiction that needs resolving now.

### Out-of-scope: `/api/webhook*` and `/metrics` (Prometheus)

The web-delivery-ui route calls out `/api/webhook*` (`GET /api/webhook`, `POST /api/webhook`, `POST /api/webhook/test`, `POST /api/webhook/revert`) as the *other* sanctioned write surface alongside `/api/clients*`. The mcp-server route's endpoint enumeration does not mention it at all — the route was scoped explicitly around "manage WireGuard peers and read metrics/status," and webhook configuration (alert-delivery URL management) is neither peer management nor metrics/status reading. This document treats `/api/webhook*` as **out of scope for this tool surface** — it is a distinct concern (alerting configuration) that the mcp-server route never named, and adding it would silently grow the mission beyond what was approved. If the owner wants webhook management exposed to the agent, that should be a deliberate route/mission-scope amendment, not something Phase 1 backs into by default.

`GET /metrics` (Prometheus text exposition, `server.go:311`) is likewise excluded: it is a scrape endpoint for external monitoring tooling, not an operator-facing read the agent needs, and it duplicates the JSON `/api/metrics*` data already covered above.

## Tool granularity decision

**Decision: one MCP tool per endpoint (method + path pair), not a coarser grouped tool.**

Rationale:

- **Read-only/mutating stays a per-tool property.** Phase 2 ships "read-only tools only" as a hard gate before any mutating tool exists. If peer CRUD were collapsed into a single `manage_client(action: "add"|"update"|"delete")` tool, that one tool would be mutating in its entirety — Phase 2 could not ship any part of it, and Phase 3 could not selectively enable, say, `add_client` without also exposing `delete_client`. One tool per endpoint means each tool's read-only/mutating classification is a fixed, inspectable fact of the tool itself, matching exactly how the phase gate is defined in the route ("Phase 2 — read-only tools only," "Phase 3 — mutating CRUD tools").
- **LLM discoverability.** MCP tool descriptions are what the calling LLM uses to decide which tool to invoke. A single `manage_client` tool with an `action` enum pushes branching logic into the tool description and the argument schema, which is a worse fit for how models select tools than four narrowly-described tools (`list_clients`, `add_client`, `update_client`, `delete_client`) that each map onto one clear verb.
- **Clean mapping onto REST verbs.** The dashboard's HTTP surface already separates concerns by verb (`GET` list vs. `POST` add vs. `PATCH` update vs. `DELETE` remove on the same `/api/clients*` path family). A one-tool-per-endpoint wrapper is a mechanical, low-risk translation with no new branching logic to get wrong — consistent with the route's "wrapper, not new business logic" framing.
- **Exception carried forward explicitly:** the four verbs on the `/api/clients*` path family are deliberately four separate tools — `list_clients`, `add_client`, `update_client`, `delete_client` — rather than one `manage_client`, precisely so `list_clients` can ship in Phase 2 while the other three wait for Phase 3.

No exceptions beyond that are proposed: every read-only endpoint in the table above gets its own tool, and no two endpoints are merged into one tool.

## Phase-mapping note

Per the mcp-server route's roadmap (read-only before mutating is a deliberate risk-reduction invariant, not an arbitrary sequence):

| Tool | Endpoint | Phase |
|---|---|---|
| `list_clients` | `GET /api/clients` | Phase 2 |
| `get_client_config` | `GET /api/clients/{name}/config` | Phase 2 |
| `get_client_history` | `GET /api/clients/{name}/history` | Phase 2 |
| `get_metrics` | `GET /api/metrics` | Phase 2 |
| `get_system_metrics` | `GET /api/metrics/system` | Phase 2 |
| `get_traffic_metrics` | `GET /api/metrics/traffic` | Phase 2 |
| `get_client_metrics` | `GET /api/metrics/client/{pubkey}` | Phase 2 |
| `get_service_status` | `GET /api/service` | Phase 2 |
| `get_server_info` | `GET /api/server` | Phase 2 |
| `get_alerts` | `GET /api/alerts` | Phase 2 |
| `get_snapshot` | `GET /api/snapshot` | Phase 2 |
| `get_geo` | `GET /api/geo` | Phase 2 |
| `get_health` | `GET /api/health` | Phase 2 (pending owner sign-off on scope, per note above) |
| `add_client` | `POST /api/clients` | Phase 3+ |
| `update_client` | `PATCH /api/clients/{name}` | Phase 3+ |
| `delete_client` | `DELETE /api/clients/{name}` | Phase 3+ |

Phase 4 (live validation over the real tunnel, checked against Clients & Connectivity route invariants) and Phase 5 (MCP host wiring/packaging, no Docker) apply across the whole tool set once Phases 2 and 3 have shipped — they are not per-tool phases and are not re-litigated here.

## Open question (deferred to Phase 3) — do not resolve here

The mcp-server route carries forward an explicit, unresolved question from Phase 1 into Phase 3: **whether mutating tools (`add_client`, `update_client`, `delete_client`) require an explicit confirmation parameter (e.g. a `confirm: true` argument) before executing, or whether a separate dry-run tool should exist ahead of any destructive call.**

Both options are named here only to frame the decision space for Phase 3 — this document takes no position and makes no recommendation:

1. **Inline confirmation parameter** — each mutating tool accepts an argument (e.g. `confirm: true`) that must be explicitly set before the call is allowed to execute against the dashboard.
2. **Separate dry-run tool** — a distinct read-only tool (e.g. `preview_client_change`) that reports what a mutating call would do, called before the corresponding mutating tool.

This is unresolved as of 2026-07-06 and is owner-deferred to Phase 3. Nothing in this document should be read as picking between them.
