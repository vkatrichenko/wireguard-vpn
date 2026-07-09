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
| `POST /api/clients` | `add_client` | Mutating (confirm-gated) | Add a new peer (name, public key, optional address/note); applied live via `wg-sync`, no tunnel drop. |
| `PATCH /api/clients/{name}` | `edit_client` | Mutating (confirm-gated) | Edit an existing peer's name/public_key/address/note. |
| `PATCH /api/clients/{name}` (`{"enabled":true}`) | `enable_client` | Mutating (confirm-gated) | Enable a peer. |
| `PATCH /api/clients/{name}` (`{"enabled":false}`) | `disable_client` | Mutating (confirm-gated) | Disable a peer. |
| `GET /api/clients` (read-only lookup) | `preview_delete_client` | Read-only | Dry-run step ahead of `delete_client`: shows the peer's current state and issues the single-use token `delete_client` requires. |
| `DELETE /api/clients/{name}` | `delete_client` | Mutating (token-gated) | Remove a peer by name. Requires a token from a prior `preview_delete_client` call for the same name. |
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
| `GET /metrics` | `get_host_metrics` | Read-only | Host metrics with no JSON `/api/*` equivalent (per-mount disk usage percent, host CPU/memory percent, per-peer rx/tx + handshake age, alert count, build version/sha), parsed from the Prometheus text exposition into structured JSON. Task #11 — see the out-of-scope note below for why this was originally excluded, and why that call was reversed. |

### Corrections against the mcp-server route's endpoint list

- **Path-param precision.** The mcp-server route describes the peer-CRUD surface generically as `GET/POST/PATCH/DELETE /api/clients`. The actual mux registration (`server.go:298-303`) only has `GET` and `POST` on the bare `/api/clients` collection; `PATCH` and `DELETE` are scoped to `/api/clients/{name}`. The table above reflects the exact registered paths.
- **No separate enable/disable endpoint, but there are separate tools.** The dashboard's `handleUpdateClient` (`handlers_clients_admin.go`) treats `enabled` as just one of the PATCH-able fields on the same `PATCH /api/clients/{name}` endpoint (name, public_key, address, note, enabled) — there is still no `POST /api/clients/{name}/enable` or similar at the HTTP layer. Phase 1 originally proposed one `update_client` tool covering edit and enable/disable together; the owner's 2026-07-06 Phase 3 resolution (see `mcp/docs/confirmation-gates.md`) split this into three tools — `edit_client`, `enable_client`, `disable_client` — all hitting the same endpoint with a different body shape, so each tool's purpose (and its confirm-gate framing in the LLM-facing description) stays unambiguous rather than bundling "rename this peer" and "kill this peer's access" behind one argument-driven branch.
- **`delete_client` is now two tools, not one.** Phase 1 listed `delete_client` as a single mutating call. Phase 3 hardened it into a token-gated dry-run flow — `preview_delete_client(name)` (read-only, issues a short-lived single-use token) followed by `delete_client(name, token)` (redeems the token, then calls `DELETE`) — because delete is the sole irreversible verb on this surface. Full mechanics (token TTL, single-use, most-recent-wins) are in `mcp/docs/confirmation-gates.md`, not repeated here.
- **`GET /api/health` is unnamed in the route but exists in code.** Registered at `server.go:295`, ahead of every other `/api/*` entry, and it is a natural low-risk Phase 2 candidate (it is how the wrapper would sanity-check tunnel connectivity to the dashboard before calling anything else). Included above as `get_health`; flagging its absence from the route's endpoint list as a gap for the owner to bless in Phase 2, not a routes/code contradiction that needs resolving now.

### Out-of-scope: `/api/webhook*`

The web-delivery-ui route calls out `/api/webhook*` (`GET /api/webhook`, `POST /api/webhook`, `POST /api/webhook/test`, `POST /api/webhook/revert`) as the *other* sanctioned write surface alongside `/api/clients*`. The mcp-server route's endpoint enumeration does not mention it at all — the route was scoped explicitly around "manage WireGuard peers and read metrics/status," and webhook configuration (alert-delivery URL management) is neither peer management nor metrics/status reading. This document treats `/api/webhook*` as **out of scope for this tool surface** — it is a distinct concern (alerting configuration) that the mcp-server route never named, and adding it would silently grow the mission beyond what was approved. If the owner wants webhook management exposed to the agent, that should be a deliberate route/mission-scope amendment, not something Phase 1 backs into by default.

### `GET /metrics` (Prometheus) — originally excluded, added back by Task #11

Phase 1 originally excluded `GET /metrics` (Prometheus text exposition, `server.go:311`) as "a scrape endpoint for external monitoring tooling... duplicates the JSON `/api/metrics*` data already covered above." That duplication assumption turned out to be wrong for one metric family: **disk usage** (`wireguard_host_disk_percent{mount=...}`) is collected by the poller but was never wired onto any JSON `/api/*` route, so it was actually invisible through the MCP wrapper, not merely duplicated. Task #11 reversed the exclusion and added `get_host_metrics`, which fetches `/metrics` via a minimal extension to `internal/dashboard.Client` (`GetMetrics`, sharing the same `do()`/`StatusError` plumbing as every other verb) and parses the hand-rolled Prometheus text defensively (standard library only, unknown metric families ignored) into a structured result — see `internal/tools/host_metrics.go`. This is still the only tool in the surface that isn't a thin proxy of an existing JSON shape, because `/metrics` has no JSON shape to proxy.

## Tool granularity decision

**Decision: one MCP tool per endpoint (method + path pair), not a coarser grouped tool.**

Rationale:

- **Read-only/mutating stays a per-tool property.** Phase 2 ships "read-only tools only" as a hard gate before any mutating tool exists. If peer CRUD were collapsed into a single `manage_client(action: "add"|"update"|"delete")` tool, that one tool would be mutating in its entirety — Phase 2 could not ship any part of it, and Phase 3 could not selectively enable, say, `add_client` without also exposing `delete_client`. One tool per endpoint means each tool's read-only/mutating classification is a fixed, inspectable fact of the tool itself, matching exactly how the phase gate is defined in the route ("Phase 2 — read-only tools only," "Phase 3 — mutating CRUD tools").
- **LLM discoverability.** MCP tool descriptions are what the calling LLM uses to decide which tool to invoke. A single `manage_client` tool with an `action` enum pushes branching logic into the tool description and the argument schema, which is a worse fit for how models select tools than four narrowly-described tools (`list_clients`, `add_client`, `update_client`, `delete_client`) that each map onto one clear verb.
- **Clean mapping onto REST verbs.** The dashboard's HTTP surface already separates concerns by verb (`GET` list vs. `POST` add vs. `PATCH` update vs. `DELETE` remove on the same `/api/clients*` path family). A one-tool-per-endpoint wrapper is a mechanical, low-risk translation with no new branching logic to get wrong — consistent with the route's "wrapper, not new business logic" framing.
- **Exception carried forward explicitly:** the `/api/clients*` path family is deliberately several separate tools rather than one `manage_client`. This document originally floated `list_clients` shipping ahead of the mutating verbs in Phase 2; the actual Phase 2/3 scope call (see the mcp-server route) went the other way — **all** of `list_clients`, `get_client_config`, `get_client_history`, `add_client`, `edit_client`, `enable_client`, `disable_client`, `preview_delete_client`, and `delete_client` shipped together in Phase 3, so the whole `/api/clients*` surface lands as one reviewable unit instead of being split across two phases.

No exceptions beyond that are proposed: every read-only endpoint in the table above gets its own tool, and no two endpoints are merged into one tool.

## Phase-mapping note

Per the mcp-server route's roadmap (read-only before mutating is a deliberate risk-reduction invariant, not an arbitrary sequence):

| Tool | Endpoint | Phase |
|---|---|---|
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
| `get_host_metrics` | `GET /metrics` | Task #11 (post-Phase-5 addition; originally excluded as out-of-scope in Phase 1, see the note above) |
| `list_clients` | `GET /api/clients` | Phase 3 (read-only, but held back from Phase 2 to ship with the rest of `/api/clients*`) |
| `get_client_config` | `GET /api/clients/{name}/config` | Phase 3 (read-only, held back — same reason) |
| `get_client_history` | `GET /api/clients/{name}/history` | Phase 3 (read-only, held back — same reason) |
| `add_client` | `POST /api/clients` | Phase 3 |
| `edit_client` | `PATCH /api/clients/{name}` | Phase 3 |
| `enable_client` | `PATCH /api/clients/{name}` | Phase 3 |
| `disable_client` | `PATCH /api/clients/{name}` | Phase 3 |
| `preview_delete_client` | `GET /api/clients` (read-only lookup) | Phase 3 |
| `delete_client` | `DELETE /api/clients/{name}` | Phase 3 |

Phase 4 (live validation over the real tunnel, checked against Clients & Connectivity route invariants) and Phase 5 (MCP host wiring/packaging, no Docker) apply across the whole tool set once Phases 2 and 3 have shipped — they are not per-tool phases and are not re-litigated here.

## Tool annotations (Praxis Task #12)

Every one of the 20 tools in this surface (14 read-only + 6 mutating — the count is unchanged by this addition) now carries an `mcp.ToolAnnotations` hint alongside its name/description, so an MCP client can reason about read/write and reversibility without parsing the description text:

- **Read-only tools → `ReadOnlyHint: true`.** All 14: `get_metrics`, `get_system_metrics`, `get_traffic_metrics`, `get_client_metrics`, `list_clients`, `get_client_config`, `get_client_history`, `get_service_status`, `get_server_info`, `get_alerts`, `get_snapshot`, `get_geo`, `get_health`, `get_host_metrics`. `preview_delete_client` also gets `ReadOnlyHint: true` even though it lives in mutating.go alongside the six mutating tools — it only reads and issues an in-process token, it never calls the dashboard's write verbs.
- **`delete_client` → `DestructiveHint: true`, `ReadOnlyHint: false`.** The sole irreversible verb on the `/api/clients*` surface (see the confirmation-gates note below); once redeemed, the peer's keypair and history are gone for good.
- **Reversible mutations (`add_client`, `edit_client`, `enable_client`, `disable_client`) → `ReadOnlyHint: false`, `DestructiveHint: false`.** Each can be undone by another call on this same surface (a bad add via `delete_client`, a bad edit via another `edit_client`, a toggle via the opposite toggle tool).
- **`IdempotentHint: true`** is set only where re-invoking with the same arguments is a genuine no-op: `enable_client` and `disable_client` (re-applying the same enabled state), and `edit_client` (re-applying the same field values re-patches to the same state). `add_client` never gets this hint — calling it twice either fails (duplicate name/key) or creates a second peer, never a no-op.
- **`OpenWorldHint` is left unset on every tool.** Every tool in this package talks only to the closed dashboard API over the WireGuard tunnel — there is no open-ended external interaction (e.g. web search) anywhere in this surface, so this hint is never set to true; see the doc comment in `internal/tools/annotations.go` for the one-time rationale.

This is additive metadata only: no tool's arguments, return bytes, or the `add`/`edit`/`enable`/`disable` `confirm=true` gate / `preview_delete_client`→`delete_client` token gate changed. See `internal/tools/annotations.go` (the shared `readOnlyAnnotations()`/`boolPtr()` helpers) and the per-tool `Annotations:` fields in `internal/tools/tools.go`, `internal/tools/mutating.go`, and `internal/tools/host_metrics.go`.

### Rejected sibling idea: re-typing every tool's return into typed Go structs

Considered and rejected alongside Task #12. The dashboard's `/api/*` endpoints already return JSON, so every read-only tool's raw-body passthrough (`tools.go`'s `get` helper, mirrored by `post`/`patch`/`del` in `mutating.go`) is already structured — proxying the bytes through is not "unstructured" in any sense that re-typing would fix. Re-modeling every tool's output into typed Go structs would instead couple this package to the dashboard's exact JSON schema at every endpoint, which breaks the deliberate byte-faithful-wrapper invariant stated in `tools.go`'s package doc comment ("Handlers never re-model the dashboard's JSON response shapes"): any dashboard-side field addition/rename would require a lockstep MCP-side struct change instead of just working. `get_host_metrics` (`host_metrics.go`) is the sole justified exception, because its source (`GET /metrics`, Prometheus text exposition) has no JSON schema to begin with — parsing it into typed fields is the only way to make it consumable at all, not a stylistic choice this task should generalize from.

## Confirmation-gate question — resolved 2026-07-06

Phase 1 carried an open question into Phase 3: whether mutating tools need an explicit confirmation parameter, a separate dry-run tool, or neither. The owner resolved this on 2026-07-06 with a split design rather than picking one option uniformly:

- `add_client`, `edit_client`, `enable_client`, `disable_client` use an inline `confirm: true` argument — reversible operations, single call once confirmed.
- `delete_client` — the sole irreversible verb on this surface — uses the separate-dry-run-tool shape instead: `preview_delete_client` issues a short-lived, single-use token that `delete_client` must redeem.

Full mechanics (token TTL, single-use, most-recent-wins, why the split by reversibility) are documented in `mcp/docs/confirmation-gates.md`, not repeated here.
