# MCP Server Route

## TL;DR
- STATUS: Phase 1 (tool-surface design) and Phase 2 (read-only tool code) are both complete as of 2026-07-06.
- Phase 2 code is unvalidated against a live dashboard — Phase 4 live-tunnel validation has not happened yet.
- `mcp/` holds a separate Go module `wireguard-mcp`, independent of the `dashboard/` Go module by design.
- Ten read-only MCP tools now exist, each a thin HTTP GET wrapper around one dashboard `/api/*` endpoint.
- The MCP server is a thin external wrapper translating MCP tool calls into HTTP calls against the dashboard's existing `/api/*` endpoints.
- It runs laptop-side over the WireGuard tunnel like any client, never on the EC2 instance, and is never embedded in the dashboard binary.
- Transport is MCP's native stdio subprocess with one hardcoded dashboard target per instance — no Docker, no always-on HTTP/SSE listener.
- Key files: `mcp/cmd/mcp-server/main.go`, `mcp/internal/tools/tools.go`, `mcp/internal/dashboard/client.go`, `mcp/docs/tool-surface.md`.

Rules for an MCP (Model Context Protocol) integration that lets an LLM agent operate the dashboard's REST API on the operator's behalf; Phase 2 of its roadmap has shipped code.

This file is the sub-router for the MCP Server route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route grows child routes, it will point to them so the agent can traverse deeper.

## Purpose

This route owns the business rules for an MCP server that lets an LLM agent manage WireGuard peers and read metrics/status through the dashboard's already-implemented REST API, without touching the EC2-hosted dashboard binary or eroding this project's stability-first posture (see the root context-router.md STABILITY OVER FEATURES rule). The architectural decisions below came out of an owner planning conversation on 2026-07-06. As of that same date, Phase 1 (tool-surface design, `mcp/docs/tool-surface.md`) and Phase 2 (scaffold + ten read-only tools, the `mcp/` Go module) are implemented; Phase 2 code has not yet been exercised against a live dashboard over a real tunnel (that is Phase 4).

## Core Concepts

- Wrapper architecture — MCP tool calls map onto the dashboard's existing `/api/*` endpoints (`/api/clients`, `/api/metrics*`, `/api/service`, `/api/server`, `/api/alerts`, `/api/snapshot`, `/api/geo`, all already implemented per specs 019/020) rather than any new dashboard code.
- Application vs Agent boundary — context-router.md's Application/Agent-LLM split is the explicit rationale for keeping MCP transport out of the Go binary: embedding it would blur that line for no functional gain, since a wrapper can already reach every endpoint it needs.
- Placement — a local process on the operator's own laptop, reached over the WireGuard tunnel like any other tunnel client; it is never deployed to the EC2 instance.
- Repo location — the code lives inside this repo, in `mcp/`, not a standalone repo, because the project is solo-maintained and planning to open-source with a single source of truth. It inherits this repo's existing git conventions (PR-based workflow, commit prefixes, branch naming).
- Auth model — relies entirely on WireGuard tunnel membership (the dashboard binds only the tunnel IP) plus the fact that only admins know the dashboard/API exists; the dashboard has no application-layer auth (see Service & Host Health route). The owner explicitly accepted this as sufficient.
- Target addressing — a single hardcoded tunnel IP:port (`172.16.15.1:8080`, per the Service & Host Health route) per MCP server instance; one MCP server per project, never one server multiplexing several projects.
- Usage-pattern rationale — the owner runs several unrelated VPN servers for different projects and fully disconnects from one before connecting to another, so the usage window and the tunnel-connectivity window are the same window by construction; this is why a single hardcoded target is sufficient and no multi-target selector is planned.
- Transport rationale — stdio subprocess (spawned on-demand by the MCP host's `mcpServers` config) was chosen over Docker or an always-on HTTP/SSE listener because this is a single-user, only-used-while-tunneled tool; Docker's isolation/reproducible-deps benefits were judged not worth the added complexity.
- Scope — full CRUD was chosen over read-only-only: both read-only tools (metrics, service/server status, alerts, snapshot, geo) and mutating tools (add/edit/delete/enable/disable peer via `/api/clients*`).
- Phased roadmap (low-risk-first, read-only before mutating) — status as of 2026-07-06:
  - Phase 1 — tool-surface definition: map confirmed `/api/*` endpoints to discrete MCP tool names. DONE — `mcp/docs/tool-surface.md`.
  - Phase 2 — scaffold and ship read-only tools only (metrics/status/service/server/alerts/snapshot/geo), validating the MCP-to-dashboard round trip with zero mutation risk. DONE — code shipped, but unvalidated against a live dashboard.
  - Phase 3 — add mutating CRUD tools (add/edit/delete/enable/disable peer), gated per however Phase 1's confirmation-gate question resolves. NOT STARTED.
  - Phase 4 — live validation of every tool against the real dashboard over the actual tunnel (not mocked), checked against Clients & Connectivity route invariants. NOT STARTED.
  - Phase 5 — wiring and packaging: MCP host config entry, no Docker. NOT STARTED.
- Implementation module — the shipped Phase 2 code lives in `mcp/`, a separate Go module named `wireguard-mcp` (bare local-style name, mirroring the dashboard's `wireguard-dashboard` module) with no import dependency on `dashboard/`.
- SDK choice — the official `github.com/modelcontextprotocol/go-sdk`, pinned exactly at `v1.6.1` per this repo's exact-version-pin convention; chosen because it is the official SDK (maintained with Google), matches the Go stack, compiles to a single static binary, and has built-in stdio transport.
- Entry point — `mcp/cmd/mcp-server/main.go` resolves the dashboard target via `-addr` flag → `MCP_DASHBOARD_ADDR` env → compiled-in default `172.16.15.1:8080`, then runs the stdio server until SIGINT/SIGTERM triggers graceful shutdown.
- Ten Phase 2 tools shipped, each a thin GET wrapper proxying the dashboard's raw JSON response as text: `get_metrics`, `get_system_metrics`, `get_traffic_metrics`, `get_client_metrics`, `get_service_status`, `get_server_info`, `get_alerts`, `get_snapshot`, `get_geo`, `get_health`.
- Scope decision (Phase 2 vs. Phase 3 for `/api/clients*`) — the read-only client endpoints (`list_clients`, `get_client_config`, `get_client_history`) were deliberately deferred out of Phase 2 into Phase 3, even though they are read-only, so the entire `/api/clients*` surface (read-only and mutating) ships together in one reviewable unit instead of being split across two phases.

## Invariants

These rules must never be violated:
- The MCP server MUST NOT be embedded in the dashboard's Go binary — it is always a separate external process, to preserve the Application/Agent boundary.
- The MCP server MUST NOT be deployed to or run on the EC2 instance — laptop-side only, to preserve spec 020's SSH-removal/SSM-only narrowing of the box's remote-code surface.
- The MCP server MUST address exactly one hardcoded dashboard target — no per-call multi-target/multi-project host selector.
- The MCP server MUST use MCP's native stdio transport — never Docker, never an always-on HTTP/SSE listener.
- Mutating tools MUST call the dashboard's existing `/api/clients*` endpoints only — the wrapper must never talk to SQLite or `wg` directly, so the dashboard remains the sole peer-mutation authority (see Clients & Connectivity route).
- Read-only tools MUST ship and be validated (Phase 2) before any mutating tool ships (Phase 3) — the phase ordering is a deliberate risk-reduction decision, not an arbitrary sequence.
- The `wireguard-mcp` Go module MUST remain separate from `dashboard/` — no import dependency between the two modules in either direction.
- The MCP SDK dependency MUST stay pinned exactly at `v1.6.1` (`github.com/modelcontextprotocol/go-sdk`), per this repo's exact-version-pin convention.
- stdout MUST be reserved exclusively for the MCP JSON-RPC wire — all logging MUST go to stderr, so protocol framing is never corrupted.
- Tool response bodies MUST be passed through as raw JSON text, never re-modeled or re-typed, so a dashboard response-shape change never forces a matching MCP-side change.
- All of `/api/clients*` (both read-only and mutating tools) MUST ship together in Phase 3 — the three read-only client endpoints are not to be pulled forward into Phase 2 individually.

## Route-Specific Constraints

- OPEN QUESTION (unresolved as of 2026-07-06): whether mutating tools need an explicit confirmation parameter or a separate dry-run tool before a destructive call (delete/edit peer). Carried from Phase 1 into Phase 3 — not yet decided, do not assume an answer.
- No application-layer auth exists on the dashboard API today (see Service & Host Health route); the MCP wrapper inherits this and adds none by design — document this as a settled, owner-accepted risk, never as an open gap to flag or fix.
- The single-hardcoded-target design depends on the operator's own usage pattern (one VPN tunnel connected at a time, never concurrent/split-tunnel across projects); if that usage pattern ever changes, this design should be revisited.
- Phase 4 live validation must be checked against Clients & Connectivity route invariants (SQLite as live source of truth, parity with the `wg-peer` CLI), not against mocks.
- Phase 2 code has been built, vetted, and smoke-tested in isolation only — it has NOT been exercised against a live dashboard over a real WireGuard tunnel; treat any round-trip behavior as unverified until Phase 4.
- `get_health`'s inclusion in Phase 2 is pending owner sign-off on scope — this route's endpoint list never names `/api/health`, even though the dashboard registers it; do not treat its presence in code as a settled scope decision.
- Phase 3 (mutating tools) and the confirmation-gate open question above are both still unstarted/unresolved — nothing in the shipped Phase 2 code should be read as resolving them.
