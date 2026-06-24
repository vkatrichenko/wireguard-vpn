# Technical Specification: Client Config Download

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

This is a **pure dashboard-application change** — no Terraform, user-data, IAM, or security-group edits are required. Every input the feature needs is already available to the running dashboard:

- **Client roster** (name, address, public key) — from `internal/clientsfile` (reads `/etc/wireguard-dashboard/clients.json`).
- **Server public key** — from `sudo wg show wg0 public-key`. The sudoers fragment in `terraform/modules/wireguard/templates/user-data.txt` **already** grants this exact command (`NOPASSWD: /usr/bin/wg show wg0 public-key`), so no privilege change is needed.
- **Public endpoint** — the Elastic IP, already surfaced via `internal/serverinfo` (IMDSv2).
- **VPC CIDR** — read at runtime via IMDSv2 (`/latest/meta-data/network/interfaces/macs/<mac>/vpc-ipv4-cidr-block`) so the **DNS resolver** (VPC-CIDR base + 2) and the **split-tunnel VPC `AllowedIPs` block** are *derived*, never hardcoded. This keeps the config correct if the VPC CIDR changes and keeps 004 a pure-app change (no Terraform injection).

The work is: a small **pure config-builder package**, **one new HTTP handler** that wires the existing services to it, and a **Download control** added to the Clients tab. The config-builder is deliberately isolated so it can be unit-tested exhaustively without touching the host, the network, or `wg`.

Affected systems: the Go dashboard binary only (`dashboard/`). Deployment rides the existing CI/SSM pipeline unchanged.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### Architecture Changes

One new internal package plus one new route; everything else reuses existing services.

| Component | Path | Responsibility |
|-----------|------|----------------|
| `wgconfig` (new) | `dashboard/internal/wgconfig/` | Pure function: given a client + routing mode + server facts, return the `.conf` text. No I/O. |
| Config handler (new) | `dashboard/internal/server/` (new handler in existing package) | Resolve client by name, gather server facts, call `wgconfig.Build`, stream the file. |
| `wg` reader (extend) | `dashboard/internal/wg/` | Add a `PublicKey()` call shelling out to `sudo wg show wg0 public-key`, cached after first success. |
| `serverinfo` (reuse + extend) | `dashboard/internal/serverinfo/` | Already provides the public IP (IMDSv2). Add a `VPCCIDR()` read (`vpc-ipv4-cidr-block`) used to derive the DNS resolver and split `AllowedIPs`. |
| `clientsfile` (reuse) | `dashboard/internal/clientsfile/` | Already loads name/address/public-key; add a lookup-by-name helper if not present. |
| Clients tab (extend) | `dashboard/web/templates/tabs/clients.html` (+ a card partial) | Render the Download control, routing-mode choice, and the private-key hint. |

### API Contracts

**`GET /api/clients/{name}/config?mode={full|split}`**

- **Path param `{name}`** — the client name from `clients.json` (URL-safe; chosen over the base64 public key, which contains `/`, `+`, `=` and would need encoding).
- **Query param `mode`** — `full` (default if omitted/unrecognized) or `split`.
- **Success `200`** — `Content-Type: text/plain; charset=utf-8`, `Content-Disposition: attachment; filename="wg-<name>.conf"`, body = generated config.
- **`404`** — `name` not present in `clients.json`.
- **`503`** — server public key, public endpoint, **or VPC CIDR** unavailable at request time (do **not** emit a config with a blank/placeholder server field or a wrong DNS line; §2.5 of the functional spec).
- Read-only `GET`; no request body. Consistent with the existing `/api/...` surface (e.g. `/api/metrics/client/{pubkey}`).

### Logic / Algorithm — `wgconfig.Build`

Signature shape (no implementation):

```
Build(client Client, mode Mode, serverPubKey string, endpoint string, vpcCIDR string) (string, error)
```

- `Client` carries `Name` and `Address` (from `clients.json`). `endpoint` is `"<eip>:51820"`. `vpcCIDR` is the VPC primary CIDR (e.g. `10.23.0.0/16`) read from IMDSv2.
- A package helper `resolverFor(vpcCIDR) → ip` derives the AWS resolver as **network base + 2** (`10.23.0.0/16 → 10.23.0.2`). Both `DNS` and the split-tunnel VPC block derive from `vpcCIDR`; **nothing VPC-dependent is hardcoded**:

| Field | Value |
|-------|-------|
| `[Interface] PrivateKey` | `<paste your client private key here>` (literal placeholder) |
| `[Interface] Address` | `client.Address` (e.g. `172.16.15.6/32`) |
| `[Interface] DNS` | `resolverFor(vpcCIDR)` (e.g. `10.23.0.2`) — **derived** |
| `[Peer] PublicKey` | `serverPubKey` |
| `[Peer] Endpoint` | `endpoint` (`<eip>:51820`) |
| `[Peer] AllowedIPs` (full) | `0.0.0.0/0, ::/0` |
| `[Peer] AllowedIPs` (split) | `<wgSubnet>, <vpcCIDR>` (e.g. `172.16.15.0/24, 10.23.0.0/16`) — VPC block **derived** |
| `[Peer] PersistentKeepalive` | `25` |

- Named constants for the **VPC-independent** values only: WG tunnel subnet (`172.16.15.0/24` — the WireGuard overlay, set by `wg_server_net`; documented constant with a comment pointing there), listen port (`51820`), keepalive (`25`), and the placeholder string. The DNS resolver and the split VPC block are **computed from `vpcCIDR`**, not constants.
- The function stays **pure**: no `os`, no `exec`, no network. The handler reads `vpcCIDR` (and pubkey/endpoint) from the host and passes them in as plain strings — fully table-testable, including `resolverFor` across different CIDRs.

### Component Breakdown — Clients tab

- A **Download control** per client (reuses the existing per-row layout from 003 §2.3). Implementation can be a plain link/form pointing at the endpoint with the selected `mode` — no new JS framework, consistent with the htmx/server-rendered approach.
- A **routing-mode choice** (Full / Split) defaulting to Full. Since the download is a direct `GET`, the simplest form is two links ("Full" / "Split") or a small `<select>` + button; either keeps the file a normal browser download.
- A **static hint** ("Replace `PrivateKey` … the server never holds it"), rendered server-side so it's readable without JS (functional spec §2.4).

---

## 3. Impact and Risk Analysis

- **System Dependencies:**
  - `clients.json` (written by Terraform user-data) — already a dashboard dependency; this feature only reads it.
  - `sudo wg show wg0 public-key` — already permitted by the live sudoers fragment; **verify the exact path** (`/usr/bin/wg`) matches at implementation time.
  - IMDSv2 lookups via `internal/serverinfo` — public-IP (already used) plus the new `vpc-ipv4-cidr-block` read.
  - **No** dependency on Terraform changes, new IAM, or new SG rules.

- **Potential Risks & Mitigations:**
  - **Stale cached server key after rotation.** If `wg.PublicKey()` caches indefinitely and the server key rotates, downloads would carry a wrong key. *Mitigation:* cache with a bounded TTL or invalidate on dashboard restart; the service already restarts on deploy. Document the chosen TTL in code.
  - **Listen port assumption.** `51820` is the module default (`wg_server_port`), but it is a Terraform variable. *Mitigation:* keep `51820` as a named constant and add a code comment pointing at `terraform/modules/wireguard/variables.tf`; revisit if the port is ever made non-default.
  - **Endpoint shows a private/temporary IP.** During the brief window before EIP association, IMDSv2 may report a temporary public IP. *Mitigation:* the EIP is associated after the health check passes (per the module); in practice the dashboard only runs post-association. Return `503` if no public IP is resolvable rather than emitting a LAN/empty endpoint.
  - **VPC has multiple CIDRs.** A VPC can carry secondary CIDRs, but AmazonProvidedDNS is always base + 2 of the **primary** CIDR. *Mitigation:* derive the resolver from the primary CIDR (`vpc-ipv4-cidr-block`, singular); for split `AllowedIPs`, the primary CIDR is sufficient for v1 — note secondary CIDRs as a future extension. Return `503` if the CIDR can't be read rather than guessing.
  - **Information exposure.** Anyone already on the VPN can download any client's template. *Mitigation accepted:* the file contains only non-secret values (server public key, public endpoint, the client's assigned tunnel IP); no private key is ever present. Documented as accepted in the functional spec §2.6.
  - **Placeholder shipped unedited.** An operator could forget to replace `PrivateKey`. *Mitigation:* the placeholder is obviously invalid (not a real key, so `wg-quick up` fails loudly) plus the inline hint; no silent failure.

---

## 4. Testing Strategy

- **Unit (the core):** table-driven tests for `wgconfig.Build` covering Full and Split modes across **different `vpcCIDR` values** — assert the **exact** `.conf` output byte-for-byte, including the placeholder line, the **derived** `DNS`, both `AllowedIPs` variants, and that no real key ever appears. Plus dedicated `resolverFor` tests (e.g. `10.23.0.0/16 → 10.23.0.2`, a `/20`, a `10.0.0.0/24`) including an invalid-CIDR error path. This is the highest-value test surface and needs no host/network.
- **Handler tests:** using `net/http/httptest`, cover: known client + `mode=full`, `mode=split`, omitted/garbage `mode` (defaults to full), unknown client → `404`, and missing server key / endpoint / **VPC CIDR** → `503`. Inject fakes for the `wg` public-key and `serverinfo` (endpoint + VPC CIDR) lookups so the handler is tested without `sudo`/IMDSv2.
- **Filename assertion:** verify `Content-Disposition` yields `wg-<name>.conf`.
- **Manual end-to-end (cannot be fully automated in-session):** on a deployed instance, download both modes for a real client, paste the matching private key, and confirm the tunnel connects and routes (full = internet via VPN; split = VPC/peer reachable, local internet untouched, the derived VPC resolver resolves). Per project policy, this manual step must actually be performed before claiming the feature works end-to-end — a passing build does not prove the tunnel comes up.
- **Quality gate:** `make test` in `dashboard/` (and the repo `make pre-commit` for the tree) before any "done" claim.
