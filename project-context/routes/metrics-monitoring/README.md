# Metrics & Monitoring Route

## TL;DR

- Stateful delta samplers (`internal/proc`, `internal/processes`) MUST be singletons or rates/CPU% render as zero forever.
- Trend data is retained 24 hours only, at 1-minute-or-finer resolution, in a SQLite store at `/var/lib/wireguard-dashboard/metrics.db`.
- `GET /api/metrics*` serves JSON time-series for Chart.js charts.
- `GET /metrics` serves Prometheus text exposition, a distinct endpoint family from `/api/metrics*`.
- Host disk usage (`wireguard_host_disk_percent`) is exposed only via `GET /metrics`, with no JSON `/api/*` equivalent.
- Key files: `internal/proc`, `internal/processes`, `internal/disk`, `internal/netdev`, `internal/poller`, `internal/db`, `internal/server/handlers_metrics.go`.

Governs how the dashboard samples, stores, and serves host and tunnel performance trends (CPU, memory, disk, network, processes).

This file is the sub-router for the Metrics & Monitoring route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns time-series collection and trend presentation — the data behind the 24-hour CPU, memory, and network charts plus the current-value numerics. It serves the operator goal of noticing resource pressure or bandwidth changes before they become outages. All sampling is pull-based and read-only; the dashboard never alters the host.

## Core Concepts

- Delta samplers — `internal/proc` (CPU% + rx/tx byte-rate from `/proc/stat` and counters) and `internal/processes` (per-PID CPU%) hold prior readings under a mutex and compute deltas; they MUST be singletons.
- Stateless readers — `internal/disk` (statfs) and `internal/netdev` (`/proc/net/dev` wg0 row) read fresh each call.
- Background poller — `internal/poller` periodically samples and writes rows to the metrics store, independent of HTTP requests.
- Metrics store — `internal/db` over modernc.org/sqlite (CGo-free), at `/var/lib/wireguard-dashboard/metrics.db` (overridable via `DB_PATH`).
- Aggregation — `internal/p95` computes p95 summaries; metrics API endpoints serve system, traffic, and per-client series for Chart.js.
- Prometheus exposition — `GET /metrics` (`handleGetMetricsProm` in `internal/server/handlers_metrics.go`) is a hand-rolled text-exposition endpoint, distinct from the JSON `/api/metrics*` chart endpoints.
- `GET /metrics` serves two consumers: external Prometheus scraping and the MCP server's `get_host_metrics` tool, its only non-JSON data source.
- Host disk usage (`wireguard_host_disk_percent{mount=...}`) is exposed exclusively via `GET /metrics` — it has no JSON `/api/*` equivalent.

## Invariants

These rules must never be violated:
- Stateful delta services (`proc`, `processes`) are constructed exactly once and shared. Constructing per-request resets the prior counters and renders zero rates/CPU% forever.
- Trend data is retained for the last 24 hours only — no 7-day / 30-day / configurable windows. Resolution is no coarser than 1 minute.
- Metric sampling is read-only against `/proc`, statfs, and the kernel; this route never executes write or control actions on the host.
- Failure to open the metrics DB at startup is fatal — a silently empty chart is worse than crashing fast so the operator sees a clean systemd failure.

## Route-Specific Constraints

- `/proc` and statfs reads do not exist on macOS dev boxes; samplers must degrade (warn, non-fatal) so local dev still runs. Warm-samples at startup are best-effort.
- The first delta sample after process start legitimately reports zero rates — templates render cumulative/absolute fields regardless.
- `netdev` gets peer count via an injected closure over the `wg` service; it must not import `internal/wg` directly (keeps the dependency graph and tests clean).
- Chart resolution and the 24-hour window are part of the functional spec (002) acceptance criteria — do not silently change them.
