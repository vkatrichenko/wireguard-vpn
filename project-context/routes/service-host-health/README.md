# Service & Host Health Route

Governs how the dashboard reports WireGuard service health, uptime, and the server's own endpoint/identity facts.

This file is the sub-router for the Service & Host Health route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns the "is the VPN service itself healthy, and what are this server's connection details?" surface. It lets the operator judge VPN-level stability independent of the host and copy endpoint values when configuring a new client. Like the rest of the dashboard, it is read-only — it observes the service, it never restarts or controls it.

## Core Concepts

- Service status — `internal/systemd` queries `wg-quick@wg0.service` via `sudo systemctl`, returning Running/Stopped, last-start time, and derived uptime.
- Server endpoint info — `internal/serverinfo` reads IMDSv2 for the public IP plus the listening UDP port (51820) and the server public key.
- Build/identity metadata — `serverinfo.BuildInfo` (SHA, build time, Go version) injected via `-ldflags` for the About card.
- Handshake/event surface — recent service-restart and handshake events shown on the service-health detail.

## Invariants

These rules must never be violated:
- Uptime reflects time since `wg-quick@wg0` last started. If the service is currently stopped, show "Service down" in red — never a misleading duration.
- This route is read-only: no service control (restart, stop), no reboot, no destructive operation. Those are explicitly out of scope.
- A serverinfo or systemd fetch failure is NOT a page 500 — degrade by surfacing the error in that card's place; the rest of the page stays useful.
- Build metadata fields default to the sentinel "unknown" (so `go run`/`go test` without ldflags still render a stable About card) and MUST be `var`, never `const` (the `-X` linker flag no-ops on constants).

## Route-Specific Constraints

- `systemd` and `serverinfo` seams are not env-configurable; tests inject fake runners / IMDS rather than shelling out or hitting real metadata.
- IMDSv2 is the only metadata path (token-based); do not fall back to IMDSv1.
- The server public key card exposes a one-click copy-to-clipboard action — keep that affordance when editing the card.
- Production binds the WG tunnel IP `172.16.15.1:8080`; the dashboard is reachable only over the tunnel, with no public edge / auth layer (de-scoped in the 2026-05-04 v3 pivot).
