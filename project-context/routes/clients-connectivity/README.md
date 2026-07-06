# Clients & Connectivity Route

## TL;DR
- The dashboard UI (and the on-box `wg-peer` CLI) is the sole authority for peer add/edit/remove/enable-disable — never Terraform (spec 019).
- The on-box SQLite `clients` table is the live source of truth; `clients.json` is only a first-boot seed and, in `cloud` mode, an S3 backup mirror.
- Terraform seeds exactly one `admin_peer` (name + public key) for anti-lockout, only when the store is empty — it never resurrects a removed admin.
- GeoIP uses the embedded DB-IP IP-to-City Lite database (GeoIP2/MMDB), not GeoLite2 (migrated in spec 006).

Governs how the dashboard reports who is configured, who is connected, and the per-client connection facts the operator relies on — and, since spec 019, how peers are added, edited, and removed.

This file is the sub-router for the Clients & Connectivity route. It follows the same contract as the root context-router.md: it describes the business rules for this route, and if the route has child routes, it points to them so the agent can traverse deeper.

## Purpose

This route owns the join between the live peer set and the live WireGuard kernel state, and the rules for presenting each client's status, plus the peer-management write path itself. It answers the operator question "who is online, who hasn't connected recently, how much traffic has each client moved, and how do I add or remove a peer?" The dashboard UI (or the equivalent on-box `wg-peer` CLI) is the **sole** peer-management authority (spec 019) — Terraform is not a day-to-day mutation path.

## Core Concepts

- Client store — the on-box SQLite `clients` table (unique `name`/`public_key`/`address`, `enabled`, `note`, timestamps) is the runtime source of truth for peers, mutated only via the dashboard's `/api/clients*` routes or the `wg-peer` CLI (same mutation path by construction).
- Live apply — peer mutations are staged and applied via a single root `wg-sync` helper running `wg syncconf`: no instance replacement, no tunnel drop, for any add/edit/remove.
- Admin bootstrap peer — Terraform seeds exactly one `admin_peer` (name + **public** key) for anti-lockout on the AWS path, only when the store is empty; afterward it is an ordinary UI-editable peer and is never re-seeded. Standalone VPS installs have no Terraform seed at all — the first peer comes from the UI or `wg-peer add`.
- Boot-seed manifest — `/etc/wireguard-dashboard/clients.json` (`internal/clientsfile`) is retained ONLY as the first-boot seed source (the admin peer, or empty) and, in `cloud` mode, mirrors the S3 backup object; it is not the live client list. **[CONFLICT — verify before use]**: observed in code on 2026-07-06, the GeoIP map and the per-client metrics-detail lookup still read this first-boot seed rather than the live client store, so a peer added purely through the UI/`wg-peer` after boot may not surface on the geo map or in per-client metrics even though it appears in the client list and can connect. Not called out in any spec — verify current behavior before relying on this.
- Storage modes — `local` (default; the only mode on a standalone VPS) = SQLite only, no external store. `cloud` = SQLite **plus** a versioned S3 object (`clients.json`) as a pure durable **backup** (write-through on every mutation, boot restore) — Terraform never reads or reconciles this object; there is no drift detection. A missing/empty S3 object is never treated as authoritative (never a wipe).
- Client rows — the joined view-model built in `internal/server/clientrows.go`, combining the live client store + live `wg` state + GeoIP.
- Status classification — Online / Offline / Pending / Unknown, derived from last-handshake recency.
- GeoIP enrichment — `internal/geoip` resolves each peer endpoint to country/city from the embedded **DB-IP IP-to-City Lite** database (GeoIP2/MMDB schema; migrated off GeoLite2 in spec 006).
- On-box `wg-peer` CLI — a thin client over the same local dashboard HTTP API the UI uses (`add`/`remove`/`update`), for first-peer onboarding on a manual VPS without SSH-forwarding the VPN-only dashboard.

## Invariants

These rules must never be violated:
- A client with a handshake in the last 3 minutes is Online (green); older or never-seen is Offline (grey). The 3-minute threshold is the contract.
- The dashboard UI (and `wg-peer`) is the **only** peer-mutation path. Terraform never declares or reconciles the peer set beyond the single anti-lockout `admin_peer` seed-when-empty.
- Peer add/edit/remove/enable-disable must never replace the EC2 instance and must never surface as `terraform plan` drift — this is the point of spec 019's collapse to one authority.
- The server never holds a client private key, with one scoped exception: `wg-peer add` (no `--pubkey`) generates an ephemeral keypair, prints it once via `--show-config`, and discards it without persisting — never written to disk/SQLite/S3.
- The client store and live `wg` state are fetched as a pair — if either fails, surface the error and render no rows rather than a half-joined, misleading list.
- Online count for summary cards counts only handshake-active rows; Pending and Unknown peers are never counted as online.
- In `cloud` mode, a hard S3 error latches the backup off (`storeReady=false`) rather than clobbering the local list; it must self-heal on the next successful reconcile without a restart or a manual peer edit (spec 020).

## Route-Specific Constraints

- Empty state: when no clients are configured, the Clients tab shows an inline "No clients configured yet — add one with the form above" prompt alongside the add-client form — not a static Terraform pointer, and not an empty table with no path forward.
- `wg`/client-store seams are not env-configurable; tests inject fakes rather than shelling out.
- GeoIP failure must degrade gracefully (missing location), never block the client list from rendering.
- Per-client cumulative bytes come straight from the kernel counters — they reset on service restart; do not present them as all-time totals.
- `cloud`-mode S3 access is least-privilege: `Get`/`PutObject` on the single `clients.json` object plus `ListBucket` on the bucket — `ListBucket` is required so a missing-object read returns 404 (safe cold-seed) instead of 403 (which the store classifies as a hard error).
