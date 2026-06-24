# Tasks: Client Config Download

- **Functional Spec:** [functional-spec.md](./functional-spec.md)
- **Technical Spec:** [technical-considerations.md](./technical-considerations.md)
- **Stack:** Go std-lib HTTP + `html/template`/htmx (dashboard `dashboard/`). All sub-tasks → `go-fullstack`.

Each slice leaves the dashboard runnable. In-session verification is `go test` + `curl` against injected fakes (local `make run` has no `sudo wg` / IMDSv2); the real tunnel-up proof is the manual host check in Slice 4.

---

- [ ] **Slice 1: Download a full-tunnel `.conf` (with derived DNS) for a configured client via the API**
  - [ ] Create `internal/wgconfig/` with VPC-independent named constants (WG tunnel subnet `172.16.15.0/24` — set by `wg_server_net`, comment pointing there; listen port `51820`; keepalive `25`; private-key placeholder), a `resolverFor(vpcCIDR) → ip` helper (network base + 2), and `Build(client, mode, serverPubKey, endpoint, vpcCIDR)` for **full** mode using the derived `DNS`. Pure: no `os`/`exec`/network. **[Agent: go-fullstack]**
  - [ ] Unit-test `resolverFor` across CIDRs (`10.23.0.0/16 → 10.23.0.2`, a `/20`, `10.0.0.0/24`, invalid CIDR → error) and `Build` (full): assert exact `.conf` output, placeholder present, derived `DNS`, no real key anywhere. **[Agent: go-fullstack]**
  - [ ] Extend `internal/serverinfo/` with `VPCCIDR()` reading IMDSv2 `/latest/meta-data/network/interfaces/macs/<mac>/vpc-ipv4-cidr-block` (primary CIDR). **[Agent: go-fullstack]**
  - [ ] Add `wg.PublicKey()` shelling `sudo wg show wg0 public-key`, cached with a bounded TTL, behind an interface for injection (verify the `/usr/bin/wg` path matches the sudoers grant). **[Agent: go-fullstack]**
  - [ ] Add a `clientsfile` lookup-by-name helper. **[Agent: go-fullstack]**
  - [ ] Add handler `GET /api/clients/{name}/config` (full mode) wiring `clientsfile` + `wg.PublicKey` + `serverinfo` (endpoint **+ VPC CIDR**); set `text/plain; charset=utf-8` and `Content-Disposition: attachment; filename="wg-<name>.conf"`; `404` unknown client; `503` if pubkey/endpoint/VPC-CIDR is unavailable (never emit a config with a blank/wrong field). **[Agent: go-fullstack]**
  - [ ] Handler tests (`net/http/httptest` + fakes for pubkey/endpoint/VPC-CIDR): `200` (assert body + `Content-Disposition`), `404` unknown client, `503` for each missing fact. **[Agent: go-fullstack]**
  - [ ] **Verify:** `make test` in `dashboard/` green; run locally with injected fakes and `curl -i` the endpoint — confirm `filename="wg-<name>.conf"` and a valid full-tunnel body whose `DNS` matches the fake VPC CIDR's base+2. **[Agent: go-fullstack]**

- [ ] **Slice 2: Split-tunnel mode via `?mode=split`** (VPC block from the same derived CIDR)
  - [ ] Extend `wgconfig` with a `Mode` type and split `AllowedIPs = <wgSubnet>, <vpcCIDR>`; only the `AllowedIPs` line varies between modes. **[Agent: go-fullstack]**
  - [ ] Parse the `mode` query param in the handler; default to **full** on missing/unrecognized values. **[Agent: go-fullstack]**
  - [ ] Unit + handler tests: split exact output (derived VPC block), `mode=garbage` → full, assert the only diff vs. full is `AllowedIPs`. **[Agent: go-fullstack]**
  - [ ] **Verify:** `curl "…/config?mode=split"` and `"…?mode=garbage"`, assert the `AllowedIPs` lines (split = `<wgSubnet>, <vpcCIDR>`; garbage = full). **[Agent: go-fullstack]**

- [ ] **Slice 3: Clients tab Download control + private-key hint**
  - [ ] Add a per-client Download control (Full / Split actions pointing at the endpoint with the selected `mode`) reusing the 003 §2.3 row/card layout; no new JS framework. **[Agent: go-fullstack]**
  - [ ] Add the server-rendered inline hint ("Replace `PrivateKey` with this client's private key before use — the server never holds it"), readable without JS. **[Agent: go-fullstack]**
  - [ ] Confirm the empty state: no clients → no Download control (reuse 003's empty state). **[Agent: go-fullstack]**
  - [ ] **Verify:** `make run`, load the Clients tab, confirm the controls + hint render and Full/Split each download `wg-<name>.conf` with correct contents. (Browser MCP unavailable — verify render/click manually; the download itself is curl-verifiable.) **[Agent: go-fullstack]**

- [ ] **Slice 4: End-to-end on the deployed host (manual, gated)**
  - [ ] Deploy the build to EC2, download both modes for a real client, paste the matching private key, and confirm the tunnel connects and routes: full = internet via VPN; split = VPC/peer reachable, local internet untouched, the derived VPC resolver resolves. Cannot be done in-session — owner runs it; required before claiming the feature works end-to-end (per CLAUDE.md). **[Agent: go-fullstack / manual]**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 3 verification | No browser MCP available | Verify UI render/click manually; the download is curl-verifiable |
| Slice 4 | Needs a real EC2 host + a WireGuard client; not reproducible in-session | Owner runs it; treat as the required manual end-to-end per CLAUDE.md |
