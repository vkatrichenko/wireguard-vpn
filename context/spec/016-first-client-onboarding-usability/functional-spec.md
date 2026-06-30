# Functional Specification: First-Client Onboarding & Dashboard Usability Fixes

- **Roadmap Item:** Post-deployment usability fixes found during standalone first-client onboarding — print an example client config at install time, make client editing legible (inline), make the handshakes panel human-readable, and give `install.sh` a full install / update / remove lifecycle.
- **Status:** Completed
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

While bringing up the standalone (non-AWS) deployment and connecting the first client, several rough edges surfaced that each cost the operator time or confidence:

1. **No starting point for the first client.** `install.sh` prints a "WireGuard server is up" summary, but the operator still has to assemble the first client config by hand — and the first client is exactly the chicken-and-egg case (you can't reach the VPN-gated dashboard until you're already a peer). An example config in the install output removes that guesswork.
2. **The edit form is cramped.** Editing a client opens a narrow right-side drawer that overlaps the table and squeezes the fields, so the values being edited don't line up with the row they belong to.
3. **Handshakes are unreadable.** The "Recent handshakes" panel lists raw WireGuard public keys and repeats the same peer every couple of minutes, so the operator can't tell at a glance *who* was last seen.
4. **No clean lifecycle.** The script can only install. There is no first-class way to **remove** WireGuard/dashboard when no longer needed, and **rerunning** to update can clobber the live peer set (it rewrites `wg0.conf` from the `WG_PEERS` env). The operator needs install, update, and remove from one script.

**Desired outcome:** an operator can connect the first client straight from the install output, edit a client in a clear inline form, read "who connected and when" at a glance, and install / update / cleanly remove the stack with the same script — without losing the server identity or runtime-added clients on update.

**Success:** the first client connects using only what `install.sh` printed; editing a client happens in place in its row; the handshakes panel shows one named row per peer with its latest handshake; a rerun updates in place without dropping clients or tunnels, and a documented remove action tears the stack down cleanly.

---

## 2. Functional Requirements (The "What")

### 2.1 Example client config in the install output

- **As an operator, I want `install.sh` to print an example client config, so that I can connect the first client without hand-assembling it or reaching the dashboard first.**
  - **Acceptance Criteria:**
    - [x] After the "WireGuard server is up" summary, the success output includes a clearly labeled **"Example client config"** block in valid `wg-quick` format (`[Interface]` + `[Peer]`).
    - [x] The block uses **real server-derived values**: the server's public key (already computed in the script), the listen port, and the **first tunnel IP derived from `WG_SERVER_NET`** (e.g. `172.16.15.2/32` for the default subnet; the correct first host for a custom subnet).
    - [x] The `Endpoint` is shown as `<server-public-ip>:<port>` (placeholder for the host's public IP) and `PrivateKey` is a clearly-marked placeholder — it is an **example/template**, nothing is auto-generated.
    - [x] **No client keypair is generated and no private key is ever created or stored on the server.**
    - [x] The block includes a one-line hint on how to generate the client keypair off-host (`wg genkey`) and how to register the client's public key (via the dashboard, or `WG_PEERS`).
    - [x] The block is printed for both the dashboard and WG-only installs (it describes the WireGuard server, which always exists).

### 2.2 Inline client editing

- **As an operator, I want to edit a client in place within its row, so that the fields line up with the client I'm editing instead of a cramped side panel.**
  - **Acceptance Criteria:**
    - [x] Clicking **Edit** reveals an editable form **within that client's expanded row** (same column as the listing); the separate right-side drawer is no longer used for editing.
    - [x] The form exposes the same editable fields as today (name, public key, tunnel IP, note, enable/disable) with the same validation.
    - [x] **Save** applies the change live (same no-downtime apply as today) and collapses back to the row's read view; **Cancel** discards changes with no modification.
    - [x] Validation errors render **inline within the row form**, and no partial change is applied on rejection.
    - [x] Editing one client does not disrupt other rows, the client list, or the geo map.

### 2.3 Human-readable handshakes panel

- **As an operator, I want the handshakes panel to show client names, one row per peer, so that I can see at a glance who was last connected.**
  - **Acceptance Criteria:**
    - [x] Each row shows the **client name** (resolved from the public key via the client list) instead of the raw public key.
    - [x] Each peer appears **at most once**, showing its **most-recent** handshake time; the repeated rows for the same peer are gone.
    - [x] Rows are ordered **most-recent first**.
    - [x] A handshake from a public key **not** in the client list still appears, with a clear fallback label (e.g. a shortened key marked "unknown") rather than being hidden.

### 2.4 Install / update / remove lifecycle for `install.sh`

- **As an operator, I want to install, update, and cleanly remove WireGuard/dashboard with the same script, so that I can manage the box's lifecycle without manual teardown or data loss.**
  - **Acceptance Criteria:**
    - [x] **Install (fresh):** running the script with no action flag on a clean host installs WireGuard (+ dashboard when a release is pinned), exactly as today.
    - [x] **Update (rerun):** running the script with no action flag on an **already-installed** host updates everything in place — re-downloads/installs the dashboard binary and rewrites the units/helper/script — and:
      - [x] reuses the existing `/etc/wireguard/server.key` (no need to pass `WG_SERVER_PRIVATE_KEY`); the server's public key/identity is unchanged.
      - [x] preserves the dashboard client DB (runtime-added clients survive).
      - [x] does **not** overwrite a dashboard-managed `wg0.conf` with the (possibly empty) `WG_PEERS` env — the live peer set is not clobbered. _(This fixes the observed "wg peers gone after rerun" defect.)_
      - [x] does not drop active tunnels for the update.
      - [x] the running dashboard process is actually replaced by the new binary (the `enable --now` → `restart` fix).
    - [x] **Remove (`--uninstall`):** stops and disables the `wireguard-dashboard` and `wg-quick@wg0` services and removes the installed artifacts (dashboard binary, systemd units, `wg-sync` helper, sudoers entry, dashboard system user). By default it **keeps data** — the server key, `wg0.conf`, and the dashboard client DB remain, so a later reinstall keeps the same server identity.
    - [x] **Dashboard-only remove:** a narrower remove mode tears down only the dashboard (service, unit, binary, helper, sudoers, user) and leaves the WireGuard tunnel running.
    - [x] **Purge (`--purge`):** in addition to remove, deletes the server key, `wg0.conf`, and the dashboard client DB — a full wipe (a reinstall then gets a new server key; existing client configs break).
    - [x] Remove/purge are **idempotent** and safe to run when components are already absent (no hard failure on missing units/files).
    - [x] The script remains `set -euo pipefail` and shellcheck-clean across all actions.

---

## 3. Scope and Boundaries

### In-Scope

- An example (template) client config block in `install.sh`'s success output, derived from real server values.
- Replacing the right-side edit drawer with inline, in-row editing on the Clients tab.
- Resolving handshake public keys to client names and collapsing the panel to one most-recent row per peer.
- An install / update / remove (+ dashboard-only) / purge lifecycle for `install.sh`, with update preserving the server key, the dashboard client DB, and the live peer set.

### Out-of-Scope

- **Server-side key generation / QR codes** — clients are still added by pasting a public key; no auto-generated configs.
- **Dashboard authentication** — unchanged; still VPN-gated, no auth.
- **EC2 teardown** — `--uninstall`/`--purge` target the standalone-VPS host lifecycle; EC2 teardown remains `terraform destroy` (the wrapper/Terraform path is unchanged).
- **The standalone IMDS / off-AWS fix** and the **bug-3 investigation** — separate work already in flight on `fix/dashboard-standalone-imds`. Note: the *safe-rerun (no-clobber) update semantics* are specified here (2.4), even though the immediate dashboard-restart fix already shipped on that branch.
- **Service-card "active vs. actually-healthy" enhancement** — the `wg-quick` `RemainAfterExit` "active even if connectivity is broken" caveat is a separate possible improvement, not included here.
- **Other roadmap items** — Phase 7 (spec 012) remaining E2E, spec 015 Slice 7 owner E2E, and remaining open-source readiness work.
