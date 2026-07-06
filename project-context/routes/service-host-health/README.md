# Service & Host Health Route

## TL;DR
- Service/uptime/endpoint reporting is read-only; the dashboard never restarts, stops, or reconfigures the WireGuard service.
- The WireGuard server private key is instance-owned (spec 020): read from SSM at boot, or generated and stored if absent — the operator never handles it.
- The private key never enters Terraform state or the EC2 launch template; only the derived public key is Terraform-managed and dashboard-displayed.
- IMDSv2 is the only metadata path; production binds the WireGuard tunnel IP only, with no public listener.

Governs how the dashboard reports WireGuard service health, uptime, and the server's own endpoint/identity facts.

This file is the sub-router for the Service & Host Health route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns the "is the VPN service itself healthy, and what are this server's connection details?" surface. It lets the operator judge VPN-level stability independent of the host and copy endpoint values when configuring a new client. Like the rest of the dashboard, it is read-only — it observes the service, it never restarts or controls it.

## Core Concepts

- Service status — `internal/systemd` queries `wg-quick@wg0.service` via `sudo systemctl`, returning Running/Stopped, last-start time, and derived uptime.
- Server endpoint info — `internal/serverinfo` reads IMDSv2 for the public IP plus the listening UDP port (51820) and the server public key.
- Build/identity metadata — `serverinfo.BuildInfo` (SHA, build time, Go version) injected via `-ldflags` for the About card.
- Handshake/event surface — recent service-restart and handshake events shown on the service-health detail.
- Server key ownership (spec 020) — the instance resolves its own WireGuard server private key from SSM at boot: reuse if a valid key is present, else generate and store one. Terraform declares no resource for the private key at all — a Terraform-managed `aws_ssm_parameter` would read the decrypted value back into state on every refresh even behind `ignore_changes`, defeating the goal of keeping it out of state. Only a Terraform-managed, non-secret **public**-key parameter shell exists, which the instance overwrites with the real public key at boot.

## Invariants

These rules must never be violated:
- Uptime reflects time since `wg-quick@wg0` last started. If the service is currently stopped, show "Service down" in red — never a misleading duration.
- This route is read-only: no service control (restart, stop), no reboot, no destructive operation. Those are explicitly out of scope.
- A serverinfo or systemd fetch failure is NOT a page 500 — degrade by surfacing the error in that card's place; the rest of the page stays useful.
- Build metadata fields default to the sentinel "unknown" (so `go run`/`go test` without ldflags still render a stable About card) and MUST be `var`, never `const` (the `-X` linker flag no-ops on constants).
- The server private key never enters Terraform state or the EC2 launch template; only the derived public key is Terraform-visible (non-secret SSM parameter) and dashboard-displayed.
- Replacing or rebuilding the instance must preserve the same server identity — boot reads the existing key back from SSM rather than regenerating, so existing client configs keep working.

## Route-Specific Constraints

- `systemd` and `serverinfo` seams are not env-configurable; tests inject fake runners / IMDS rather than shelling out or hitting real metadata.
- IMDSv2 is the only metadata path (token-based); do not fall back to IMDSv1.
- The server public key card exposes a one-click copy-to-clipboard action — keep that affordance when editing the card.
- Production binds the WG tunnel IP `172.16.15.1:8080`; the dashboard is reachable only over the tunnel, with no public edge / auth layer (de-scoped in the 2026-05-04 v3 pivot).
