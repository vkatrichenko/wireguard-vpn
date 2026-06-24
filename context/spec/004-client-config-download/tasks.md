# Tasks: Client Config Download

- **Functional Spec:** [functional-spec.md](./functional-spec.md)
- **Technical Spec:** [technical-considerations.md](./technical-considerations.md)
- **Stack:** Go std-lib HTTP + `html/template`/htmx (dashboard `dashboard/`). All sub-tasks → `go-fullstack`.

Each slice leaves the dashboard runnable. In-session verification is `go test` + `curl` against injected fakes (local `make run` has no `sudo wg` / IMDSv2); the real tunnel-up proof is the manual host check in Slice 4.

---

- [x] **Slice 1: Download a full-tunnel `.conf` (with derived DNS) for a configured client via the API**
  - [x] Create `internal/wgconfig/` with VPC-independent named constants (WG tunnel subnet `172.16.15.0/24` — set by `wg_server_net`, comment pointing there; listen port `51820`; keepalive `25`; private-key placeholder), a `resolverFor(vpcCIDR) → ip` helper (network base + 2), and `Build(client, mode, serverPubKey, endpoint, vpcCIDR)` for **full** mode using the derived `DNS`. Pure: no `os`/`exec`/network. **[Agent: go-fullstack]**
  - [x] Unit-test `resolverFor` across CIDRs (`10.23.0.0/16 → 10.23.0.2`, a `/20`, `10.0.0.0/24`, invalid CIDR → error) and `Build` (full): assert exact `.conf` output, placeholder present, derived `DNS`, no real key anywhere. **[Agent: go-fullstack]**
  - [x] Extend `internal/serverinfo/` with `VPCCIDR()` reading IMDSv2 `/latest/meta-data/network/interfaces/macs/<mac>/vpc-ipv4-cidr-block` (primary CIDR). **[Agent: go-fullstack]**
  - [x] ~~Add `wg.PublicKey()`~~ **Not needed** — `serverinfo.Get()` already returns the server public key (it shells `sudo /usr/bin/wg show wg0 public-key` in `fetchServerPublicKey`), so the handler reuses it; the `wg` package is untouched. **[Agent: go-fullstack]**
  - [x] Add a `clientsfile` lookup-by-name helper (`ByName`, mirroring `ByPublicKey`). **[Agent: go-fullstack]**
  - [x] Add handler `GET /api/clients/{name}/config` (full mode) wiring `clientsfile` + `serverinfo.Get` (pubkey + endpoint) + `serverinfo.VPCCIDR`; set `text/plain; charset=utf-8` and `Content-Disposition: attachment; filename="wg-<name>.conf"`; `404` unknown client; `503` if pubkey/endpoint/VPC-CIDR is unavailable (never emit a config with a blank/wrong field). **[Agent: go-fullstack]**
  - [x] Handler tests (`net/http/httptest` + fakes for pubkey/endpoint/VPC-CIDR): `200` (assert body + `Content-Disposition`), `404` unknown client, `503` for missing server-info and `503` for missing VPC-CIDR. **[Agent: go-fullstack]**
  - [x] **Verify:** `go test ./...` in `dashboard/` green (all packages `ok`, incl. new `wgconfig` + `server` config tests); `gofmt` clean; `go vet` clean. The httptest handler suite exercises the same fake-backed path a local `curl` would (no live `make run` — local dev has no IMDS/sudo). **[Agent: go-fullstack]**

- [x] **Slice 2: Split-tunnel mode via `?mode=split`** (VPC block from the same derived CIDR)
  - [x] Extend `wgconfig` with `ModeSplit` + a `ParseMode` helper and the split `AllowedIPs = <wgSubnet>, <vpcCIDR>` case; only the `AllowedIPs` line varies between modes. (The `Mode` type itself landed in Slice 1.) **[Agent: go-fullstack]**
  - [x] Parse the `mode` query param in the handler via `wgconfig.ParseMode(r.URL.Query().Get("mode"))`; defaults to **full** on missing/unrecognized values. **[Agent: go-fullstack]**
  - [x] Unit + handler tests: split exact output (derived VPC block), `mode=garbage` → full, `ParseMode` table, and an invariant asserting the only diff vs. full is the `AllowedIPs` line. **[Agent: go-fullstack]**
  - [x] **Verify:** handler tests assert `?mode=split` → `AllowedIPs = 172.16.15.0/24, 10.23.0.0/16` and `?mode=garbage` → full `0.0.0.0/0, ::/0`; `go test ./...` green, `gofmt`/`go vet` clean. **[Agent: go-fullstack]**

- [x] **Slice 3: Clients tab Download control + private-key hint**
  - [x] Add a per-client Download control — a new "Config" column with Full / Split `<a download>` links pointing at `/api/clients/{name}/config?mode=…`, with `event.stopPropagation()` so a download click doesn't also toggle the row-expand. Only rows with a manifest name show links (unknown peers render "—"). No new JS framework. **[Agent: go-fullstack]**
  - [x] Add the server-rendered inline hint ("…replace `PrivateKey` with that client's private key before use — the server never holds it"), shown whenever rows exist, readable without JS. **[Agent: go-fullstack]**
  - [x] Confirm the empty state: no clients → no Download control (the control lives inside the `{{ if .Rows }}` branch; the existing `TestHandleGetPartialTabs/clients` empty-state test still passes). **[Agent: go-fullstack]**
  - [x] **Verify:** new `TestHandleGetPartialClients_DownloadControl` renders `/partial/clients` and asserts the `<th>Config</th>` header, both `?mode=full`/`?mode=split` hrefs keyed by name, and the hint; existing clients/empty-state tests still pass; `go test ./...` green, `gofmt` clean. (No live `make run` — no browser MCP; the rendered markup + the curl-equivalent API tests from Slices 1–2 cover it.) **[Agent: go-fullstack]**

- [ ] **Slice 4: End-to-end on the deployed host (manual, gated)**
  - [ ] Deploy the build to EC2, download both modes for a real client, paste the matching private key, and confirm the tunnel connects and routes: full = internet via VPN; split = VPC/peer reachable, local internet untouched, the derived VPC resolver resolves. Cannot be done in-session — owner runs it; required before claiming the feature works end-to-end (per CLAUDE.md). **[Agent: go-fullstack / manual]**

---

## Items needing attention

| Task/Slice | Issue | Recommendation |
|---|---|---|
| Slice 3 verification | No browser MCP available | Verify UI render/click manually; the download is curl-verifiable |
| Slice 4 | Needs a real EC2 host + a WireGuard client; not reproducible in-session | Owner runs it; treat as the required manual end-to-end per CLAUDE.md |
