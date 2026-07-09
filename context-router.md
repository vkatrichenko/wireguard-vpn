# context-router.md Purpose

- context-router.md serves as the mandatory primary entry point and high-level compass for any LLM or AI agent interacting with this repository. It functions as a root router, directing the AI to the appropriate route-specific documentation to understand the project architecture, business logic, and execution rules. It ensures the AI loads only the necessary context by branching down into specific sub-routers.
- context-router.md is the Behavioral Rulebook and Architectural Compass for all AI agents or human-being working on this project.
- IMPLEMENTATION IS THE MAP, ROUTER IS THE COMPASS: Use the files_tree.md to find WHERE files are. Use this context-router exclusively to understand WHY the architecture exists, HOW logical routes interact, and the strict rules you must follow.
- TRIGGER RULE: Whenever you work on a specific feature or entity, you MUST consult its corresponding route directory in this router to understand its business logic and constraints BEFORE making decisions, and completing the task.
- We don't need any other context, history, memory. We're starting from scratch!

# context-router.md terminology

- Context Router: This root file, providing a high-level map of the entire project.
- Route: A specific directory representing a business or technical module. It acts as a node in our architecture tree. Every route directory inherently contains its own context and must include a README.md file, which acts as its sub-router.
- Sub-router: The README.md file located inside any route directory. It acts exactly like the root context router but is isolated to its specific route. A sub-router can declare its own child routes (sub-directories), creating an infinitely deep nested structure where every node follows the same routing contract.
- Route: A named pointer from a router (root or sub-router) to a route directory. The directory contains a README.md sub-router that may itself declare further routes to child routes.

# Hierarchical routing principle

The routing system is a recursive tree with no depth limit. The flow is always: Router -> Route (Directory) -> Sub-router (README.md) -> Child Route (Directory) -> Child Sub-router (README.md), infinitely. Every node follows the exact same contract. Never skip a level when traversing. A route is never just a dead-end folder; if it has complexity, its sub-router will point you to the deeper child routes you need.

# Project terminology
- Application: the core service codebase running without any AI involvement. Here that is the Go single-binary WireGuard dashboard plus the Terraform that deploys it. When a requirement states "Application must do X", it means the standard code handles X with no LLM or agent participation.
- Agent / Copilot / LLM: an AI-powered component or external AI tool performing a task. When a requirement states "Agent must do X" or "LLM must do X", it means an AI model or AI-assisted tool handles X, not the core application code.

# Mandatory Execution Rules
- Always start context gathering from this file.
- Never guess or hallucinate business logic. You must navigate to the relevant route directory and read its sub-router (README.md) to acquire the correct context.
- Traverse the routing tree recursively. Every route directory may contain a README.md sub-router that declares its own child routes. Follow each relevant route downward, reading sub-routers at every level, until you reach the granularity required for the task. There is no depth limit; the hierarchy branches as deep as the project requires.
- SERVICE CONTROL IS OUT OF SCOPE: The dashboard observes the VPN and host and never restarts, stops, or reconfigures the WireGuard service. Peer management is different: since spec 019 the dashboard UI (and the on-box `wg-peer` CLI) is the sole authority for adding, editing, and removing WireGuard peers via `wg syncconf` — Terraform only seeds one anti-lockout `admin_peer` and is never the day-to-day mutation path. See the Clients & Connectivity route for peer-management rules.
- STABILITY OVER FEATURES: This project's success metric is operational reliability. Prefer fail-fast on unrecoverable startup errors, graceful per-card degradation at request time, and read-only sampling that never destabilizes the host. Do not trade these for new surface area.
- Load Selectively: Open ONLY the specific documentation directories strictly required for your task/role. Do not load the entire project context.
- UPDATE TRIGGER (CRITICAL): If your task changes the fundamental business logic, data structure, or rules of a route (e.g., adding a new mandatory field to a core entity), you MUST update the corresponding route documentation in project-context/ to reflect this new reality.
- Avoid files more than 500 strings in size for better performance and reliability.

# Project context map (Routes)

## Product Requirements Document (PRD) & Vision
The absolute source of truth for the product vision. It explains WHAT we are building, WHO we are working for, and the core problems we solve.
ALWAYS read this if your task involves planning new features, writing documentation, or understanding the product's core value proposition.
File Path: context/product/product-definition.md

## Project description
A self-hosted WireGuard VPN on AWS EC2 (provisioned with Terraform) plus an ops dashboard shipped as a single static Go binary deployed alongside the WireGuard server. The dashboard surfaces tunnel and host health — client connection status, CPU/memory/disk/network trends, service uptime, and server endpoint info — in one auto-refreshing, mobile-responsive page, and is the sole authority for adding, editing, and removing WireGuard peers (spec 019), so the solo operator can run day-to-day VPN operations without an SSH session. Stack: Go standard-library HTTP + html/template + embed.FS, htmx server-rendered partials, Chart.js, modernc.org/sqlite for the 24-hour trend store and the runtime client store, /proc and IMDSv2 reads, an embedded DB-IP IP-to-City Lite database for client geolocation; infrastructure is Terraform (AWS provider, exact-pinned). It binds the WireGuard tunnel IP and is reachable only over the VPN — no public edge or in-band auth. Access to the host itself is via SSM Session Manager only (no SSH). Audience: solo-maintained today, planned to be open-sourced later.

# Core Business Routes (Behavioral Rules)
ALWAYS read the corresponding route directory before proceeding with the task related to it. Use these to understand the required elements and business logic.

## Clients & Connectivity Route
Rules for the UI-authoritative peer store (SQLite, spec 019) joined with live WireGuard kernel state — peer add/edit/remove, online/offline classification, handshake recency, per-client traffic, and GeoIP enrichment.
Directory Path: project-context/routes/clients-connectivity/

## Metrics & Monitoring Route
Rules for sampling, storing, and serving host/tunnel performance trends — CPU, memory, disk, network, and process metrics, the background poller, and the 24-hour SQLite trend store.
Directory Path: project-context/routes/metrics-monitoring/

## Service & Host Health Route
Rules for reporting WireGuard service status and uptime, server endpoint/identity info, and build metadata — read-only, never service control.
Directory Path: project-context/routes/service-host-health/

## Web Delivery & UI Route
Rules for the HTTP surface, view-models, htmx partials, embedded assets, auto-refresh, and the responsive UI that presents the data routes and hosts the sanctioned client-management and webhook-management write endpoints.
Directory Path: project-context/routes/web-delivery-ui/

## MCP Server Route
Implemented and shipped: rules for a laptop-side MCP server that wraps the dashboard's existing REST API so an LLM agent can manage peers and read metrics over the WireGuard tunnel, live-tunnel validated and publicly distributable via go install.
Directory Path: project-context/routes/mcp-server/

## Alerting & Notifications Route
Rules for the in-memory alert evaluator (service-down, high-disk, high-cpu, transfer-cap conditions with cooldown-suppressed re-notification) and the transport-agnostic outbound notifier (webhook, Slack bot, Telegram, Discord) plus its runtime-mutable webhook management endpoints.
Directory Path: project-context/routes/alerting-notifications/

## AI Skills and Agents
Available tools and automated skills for the AI agent (e.g., context-router initializer/actualizer setup scripts).
Directory Path: .agents/skills/

# Rules for this file (context-router.md)

- Never use bold formatting in this file.
- Keep this file clear and concise.
- Never add granular feature-level routes to this file. Rely on files_tree.md for detailed routing.
- For proper update of this file you MUST ALWAYS use .agents/skills/context-router-actualizer/SKILL.md
