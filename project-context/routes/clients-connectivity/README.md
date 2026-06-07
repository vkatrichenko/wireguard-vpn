# Clients & Connectivity Route

Governs how the dashboard reports who is configured, who is connected, and the per-client connection facts the operator relies on.

This file is the sub-router for the Clients & Connectivity route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns the join between the desired client list and the live WireGuard kernel state, and the rules for presenting each client's status. It answers the operator question "who is online, who hasn't connected recently, and how much traffic has each client moved?" Terraform's `clients_config` is the single source of truth for which clients exist — this route is read-only over that truth and never mutates it.

## Core Concepts

- Client manifest — `/etc/wireguard-dashboard/clients.json`, rendered from Terraform `clients_config`, read by `internal/clientsfile`.
- Live peer state — `wg show wg0 dump` output (last handshake, cumulative rx/tx bytes, endpoint) read by `internal/wg`.
- Client rows — the joined view-model built in `internal/server/clientrows.go`, combining manifest + live state + GeoIP.
- Status classification — Online / Offline / Pending / Unknown, derived from last-handshake recency.
- GeoIP enrichment — `internal/geoip` resolves each peer endpoint to country/city from the embedded GeoLite2 database.
- Runtime reconcile (Draft, spec 004) — moving the client list to SSM and adding a "Refresh & Apply" control using `wg syncconf` for zero-downtime peer changes.

## Invariants

These rules must never be violated:
- A client with a handshake in the last 3 minutes is Online (green); older or never-seen is Offline (grey). The 3-minute threshold is the contract.
- The dashboard is read-only over clients: it never adds, removes, or regenerates clients. Terraform `clients_config` remains the only mutation path (until the spec-004 reconcile lands, which still keeps Terraform as the source of truth).
- The manifest and live `wg` state are fetched as a pair — if either fails, surface the error and render no rows rather than a half-joined, misleading list.
- Online count for summary cards counts only handshake-active rows; Pending and Unknown peers are never counted as online.

## Route-Specific Constraints

- Empty state: when no clients are configured, show "No clients configured. Add via `terraform/dev/main.tf`." — do not render an empty table.
- `wg`/`clientsfile` seams are not env-configurable; tests inject fakes rather than shelling out.
- GeoIP failure must degrade gracefully (missing location), never block the client list from rendering.
- Per-client cumulative bytes come straight from the kernel counters — they reset on service restart; do not present them as all-time totals.
