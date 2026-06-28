# Tasks: Alert Transports & Prometheus Metrics

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)

Slices are ordered so the dashboard builds and tests stay green after each one. Verification is unit-test/build-based (`go-fullstack` runs `go test`/`vet`/build; `terraform-aws` runs `fmt`/`validate`/`plan`); no deployed environment is needed until owner E2E.

---

- [x] **Slice 1: Remove the peer-down alert condition**
  - [x] Excise peer-down from `internal/alerts` — `ConditionPeerDown`, `peerDownObservation()` + its call site, `seenOnline`, the peer thresholds + `Default*` consts, `Config.PeerStaleThreshold`; **keep** the per-peer loop (transfer-cap uses it). **[Agent: go-fullstack]**
  - [x] Remove the `DASHBOARD_ALERT_PEER_STALE` read in `cmd/wireguard-dashboard/main.go`. **[Agent: go-fullstack]**
  - [x] Update tests: drop peer-down tests (incl. the poller `TestPoller_PeerDownDispatchedKeyedByName`, covered by the transfer-cap dispatch test); add `TestStalePeerNeverFires` regression; four remaining conditions still fire/recover. **[Agent: go-fullstack]**
  - [x] Verify: `gofmt`/`go vet` clean; `go test -race -count=1 ./...` all green; `CGO_ENABLED=0 GOOS=linux GOARCH=amd64` build OK. **[Agent: go-fullstack]**

- [x] **Slice 2: Fan-out composite + shared HTTP helper**
  - [x] Factor the webhook sender's post-JSON-with-timeout + bounded-retry into a shared `internal/notify` helper (`postJSON`/`httpRetry`; `Webhook` refactored to use it, behavior preserved). **[Agent: go-fullstack]**
  - [x] Add `MultiNotifier` implementing `Notifier` (slice of notifiers; `errors.Join` aggregation; isolates per-transport failures). **[Agent: go-fullstack]**
  - [x] Wire `main.go` to wrap the existing webhook notifier in a `MultiNotifier` (webhook-only for now → behavior unchanged). **[Agent: go-fullstack]**
  - [x] Verify: `MultiNotifier` tests (isolation + aggregation, all-succeed, nil/empty-safe) + all existing webhook tests pass; `gofmt`/`vet` clean; `go test -race` + static build green. **[Agent: go-fullstack]**

- [x] **Slice 3: New transports (Slack bot, Telegram, Discord)**
  - [x] Add the **Slack bot** transport (`chat.postMessage`, Bearer token, `{channel,text}`, `ok:false`→error via a `postJSON` body-validator hook), env-configured, no-op when unset, secret redacted. **[Agent: go-fullstack]**
  - [x] Add the **Telegram** transport (`sendMessage`, `{chat_id,text}`, token-in-path redacted), env-configured, no-op when unset. **[Agent: go-fullstack]**
  - [x] Add the **Discord** transport (webhook URL, `{content}`, 204→success), env-configured, no-op when unset, redacted. **[Agent: go-fullstack]**
  - [x] Wire all three into the `MultiNotifier` from env in `main.go` (untyped-nil constructors filtered; enabled transport names logged, no secrets). **[Agent: go-fullstack]**
  - [x] Verify: per-transport unit tests (URL/headers/payload; Slack `ok:false`→error; Discord 204); redaction + no-op-when-unconfigured tests; fan-out test; `go test -race` + static build green. **[Agent: go-fullstack]**

- [x] **Slice 4: Prometheus `/metrics` endpoint**
  - [x] Add `poller.MetricsSnapshot()` (mutex-guarded last sample: service, peers online/total + per-peer age/rx/tx, cpu/mem/disk%). **[Agent: go-fullstack]**
  - [x] Add a nil-tolerant `MetricsProvider` to the server + active-alert count from `StatusHolder`; register `GET /metrics` with the hand-rolled exposition handler (namespace `wireguard_`, `# HELP`/`# TYPE`, escaped labels). **[Agent: go-fullstack]**
  - [x] Wire the poller as the provider in `main.go`. **[Agent: go-fullstack]**
  - [x] Verify: handler unit test over a fake snapshot (format, key metrics, label escaping); route registered; no per-scrape exec/DB; tests/build green. **[Agent: go-fullstack]**

- [x] **Slice 5: Terraform wiring + docs**
  - [x] Terraform: remove `dashboard_alerts.peer_stale` (variables.tf + locals threading + the `DASHBOARD_ALERT_PEER_STALE` line in `templates/user-data.txt`). **[Agent: terraform-aws]**
  - [x] Terraform: add opt-in SSM-seeded transport secrets (count-gated `data.aws_ssm_parameter` + plain vars for channel/chat-id) threaded into `alerts.env` only when set — Slack bot token+channel, Telegram token+chat-id, Discord webhook URL. **[Agent: terraform-aws]**
  - [x] Docs: update `context/product/architecture.md` §8 (five→four conditions; add the new transports + the `/metrics` endpoint). **[Agent: terraform-aws]**
  - [x] Verify: `terraform fmt -recursive` + `validate` in `terraform/dev/`; `plan` shows no unexpected diff (empty-default params = no change); `make pre-commit`. (Apply is owner-run.) **[Agent: terraform-aws]**

---

## Notes & accepted exceptions

| Task/Slice | Issue | Resolution |
|------------|-------|------------|
| Slice 5 verify | `terraform apply` is manual/owner-run | Agent runs `fmt`/`validate`/`plan`; owner applies |
| Transport delivery E2E | Real delivery needs a deployed instance + real Slack-bot/Telegram/Discord credentials | Owner verifies live delivery post-deploy; agent verification is injected-HTTP-doer unit tests |
| `/metrics` scrape E2E | Live scrape needs a deployed instance + a Prometheus scraper on the VPN | Owner verifies live scrape; agent verifies the exposition format via the handler unit test |

## Deferred to verify

- `context/product/product-definition.md` §3.2 update (multi-transport alerting + metrics endpoint; still no email/SMS/PagerDuty, still no auth) — applied at `/awos:verify`.
