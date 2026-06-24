# `internal/geoip` — offline IP-to-country/city lookup

The dashboard's Clients tab resolves peer endpoint IPs to a country + city
via an offline **DB-IP IP-to-City Lite** database, embedded into the binary at
build time via `embed.FS`. The lookup is microsecond-fast, requires zero
network access at runtime, and produces no telemetry to a third party.

DB-IP IP-to-City Lite ships in the MaxMind GeoIP2-compatible MMDB format, so
the `github.com/oschwald/geoip2-golang` reader is used unchanged. It replaced
MaxMind GeoLite2, which is no longer freely redistributable (post-2019 GeoLite2
is governed by MaxMind's proprietary EULA, not CC BY). DB-IP Lite is **CC BY
4.0** — attribution only, no ShareAlike, freely redistributable.

## Files

| File | Purpose |
|---|---|
| `dbip-city-lite.mmdb` | The DB-IP binary database (~124 MB). **Fetched at build, not committed** — gitignored; see below. |
| `LICENSE-DB-IP.txt` | CC BY 4.0 attribution text required by DB-IP. |
| `geoip.go` | The Go wrapper around `oschwald/geoip2-golang`. |
| `geoip_test.go` | Lookup tests run against the real embedded database. |
| `README.md` | This file. |

## Fetch-at-build (the `.mmdb` is not committed)

The ~124 MB database is **not** checked in (it's `.gitignore`d) — it's fetched
at build time and embedded into the binary. The free DB-IP Lite files download
directly with no account or license key.

- **Local dev:** `make build`, `make run`, and `make test` all depend on the
  database and auto-fetch it on first use (`make geoip-db` to fetch explicitly).
  The file is cached on disk after the first fetch, so subsequent builds are
  offline.
- **CI:** the release workflow (`.github/workflows/dashboard-release.yml`) runs
  `make geoip-db` before the Go steps, so **every tagged release embeds the
  current month's database** — no manual refresh, no stale snapshot.
- **Month override:** the Makefile defaults to the current month
  (`dbip-city-lite-$(date +%Y-%m).mmdb.gz`). If a release is cut before DB-IP
  has published the new month, pass `GEOIP_DB_MONTH=YYYY-MM`, e.g.
  `make geoip-db GEOIP_DB_MONTH=2026-05`.

There is **no runtime auto-update loop** — the database is frozen at build time,
so a bad DB-IP release cannot silently change behavior on a running host; rolling
a new database is a deliberate new tagged release.

## Licensing notes

- The DB-IP IP-to-City Lite database is licensed **CC BY 4.0** (see
  `LICENSE-DB-IP.txt`). The required attribution — **"IP Geolocation by DB-IP"**
  with a link to <https://db-ip.com> — is satisfied by shipping
  `LICENSE-DB-IP.txt` alongside the database; if you surface the lookup result
  in a public-facing UI element, include the attribution string visibly (e.g.
  the dashboard footer).
- CC BY 4.0 has **no ShareAlike clause**, so embedding the unmodified database
  does not affect the license of the surrounding source code — and the database
  may be redistributed freely (including in this public repository) as long as
  the attribution is preserved.

## Why not call a hosted geolocation API instead?

- **No outbound network from the EC2** — the WireGuard host's egress is
  intentionally narrow (IMDS + the boot-time GitHub release fetch). Adding a
  hosted geolocation API would introduce a runtime dependency on a third party.
- **No telemetry leak** — peer endpoint IPs would otherwise be sent to the
  geolocation provider on every dashboard refresh.
- **Predictable latency** — the embedded lookup resolves in ~µs vs. a
  50–200 ms network call.

The ~124 MB binary-size cost is the price of those properties — acceptable for
a self-hosted single-instance VPN dashboard.
