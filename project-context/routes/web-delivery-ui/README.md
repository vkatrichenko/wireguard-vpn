# Web Delivery & UI Route

## TL;DR
- The HTTP surface is not fully read-only: `/api/clients*` (peer CRUD, spec 019) and `/api/webhook*` (spec 008) are sanctioned write endpoints.
- Templates parse eagerly at startup from an embedded `web/` FS — a malformed template must fail boot, not a request.
- A failed per-card view-model fetch degrades to an inline error block; it never turns into a whole-page 500.
- Static assets are fully vendored and embedded — no CDN, no runtime disk reads, offline-capable by design.

Governs how the dashboard is served — HTTP routing, view-models, server-rendered htmx partials, embedded assets, and the auto-refreshing responsive UI.

This file is the sub-router for the Web Delivery & UI route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns the delivery layer that turns the data routes (clients, metrics, service) into a single-page operator view. It defines the HTTP surface, how handlers assemble per-card view-models, and how the browser refreshes live values without a reload. Success is the operator answering "is the VPN healthy?" in under 10 seconds from any device.

## Core Concepts

- HTTP mux — `internal/server/server.go` wires a stdlib `*http.ServeMux`: `/api/*` JSON/HTML endpoints, `/partial/*` htmx fragment endpoints, `/static/`, and the `/` index.
- Handlers — split by domain (`handlers_clients.go`, `handlers_metrics.go`, `handlers_service.go`, `handlers_server.go`, `handlers_partial_tabs.go`, `handlers_snapshot.go`).
- View-models — `pageData` and per-card structs assembled in `buildPageData`; per-card error strings degrade individual cards instead of failing the page.
- Templates — `web/templates/` pages + `tabs/` + `cards/` (and `cards/charts/`) parsed eagerly at startup via `embed.FS` so a malformed template fails fast.
- Front-end assets — vendored htmx, Chart.js + date-fns adapter, `app.css`, `theme.js`, `tabs.js`, `charts.js`, served from `web/static/`.
- Tabs — Overview, Clients, System, Network, Events, About.

## Invariants

These rules must never be violated:
- Templates are parsed eagerly at startup; a malformed template must fail fast on boot, not on first request.
- A single failed card fetch degrades to a per-card error block — it never turns into a whole-page 500 once the page itself is renderable.
- The dashboard auto-refreshes data every 10 seconds; on a failed refresh the previously rendered values stay visible behind a "Stale data" indicator until the next success.
- Layout is mobile-responsive: no horizontal scroll at ≥360px, cards/charts re-flow to one column below ~600px, touch targets ≥44px.
- The UI is read-only over metrics, service status, and connectivity views; it never issues service-control actions (restart/stop). The client-management (`/api/clients*`, spec 019) and webhook-management (`/api/webhook*`, spec 008) endpoints are the sanctioned write paths — see the Clients & Connectivity route for peer-mutation rules.

## Route-Specific Constraints

- Static assets are vendored and embedded (`//go:embed all:web`) — no CDN, no runtime disk reads, no third-party static-file library. Record provenance in `web/static/VENDORED.txt`.
- `//go:embed` paths cannot climb out of their directory with `..`; the `fs.FS` is rooted at `web/` and passed into `server.New` for testability with `fstest.MapFS`.
- New service dependencies are appended to the end of `server.New`'s parameter list — never reordered — so existing call sites only append.
- The `GET /` pattern is a catch-all; handlers must explicitly 404 anything that isn't exactly `/`.
- Byte/rate/duration formatting goes through the shared helpers (`humanBytes`, `humanBytesPerSec`, `humanUptime`, `humanAgo`, `humanDuration`) for one source of truth.
