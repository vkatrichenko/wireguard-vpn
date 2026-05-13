# `internal/geoip` — offline IP-to-country/city lookup

The dashboard's Clients tab resolves peer endpoint IPs to a country + city
via an offline MaxMind GeoLite2-City database, embedded into the binary at
build time via `embed.FS`. The lookup is microsecond-fast, requires zero
network access at runtime, and produces no telemetry to a third party.

## Files

| File | Purpose |
|---|---|
| `GeoLite2-City.mmdb` | The MaxMind binary database (~7 MB). Replaced on refresh. |
| `LICENSE-GeoLite2.txt` | CC BY-SA 4.0 attribution text required by MaxMind. |
| `README.md` | This file. |
| `geoip.go` *(future)* | The Go wrapper around `oschwald/geoip2-golang`. Added in Slice 4 sub-task 3. |

## Refresh procedure

MaxMind ships weekly updates. The bundled snapshot can lag for months
without practical impact — country/city assignments rarely change. Refresh
when convenient:

1. Sign in to your MaxMind account → **My Account** → **Download Files** →
   **GeoLite2 City** → choose the `Binary / gzipped` format → download.
   (A free MaxMind account + license key is required; sign up at
   [maxmind.com](https://www.maxmind.com/en/geolite2/signup) if you don't
   have one.)
2. Extract the archive — inside is a directory like
   `GeoLite2-City_YYYYMMDD/GeoLite2-City.mmdb`.
3. Replace `dashboard/internal/geoip/GeoLite2-City.mmdb` with the new file
   in place. Keep the same filename.
4. Rebuild the dashboard binary:
   ```sh
   cd dashboard
   CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
     -ldflags "-s -w" \
     -o wireguard-dashboard \
     ./cmd/wireguard-dashboard
   ```
   In CI, the standard `dashboard-build.yml` workflow picks up the new
   `.mmdb` automatically — no workflow changes required.
5. Deploy via the existing GitHub Actions `dashboard-deploy.yml` workflow,
   or run it manually with `workflow_dispatch`.

There is **no auto-update loop**. Refresh is a deliberate, reviewable
commit so a bad database release cannot silently land in production.

## Licensing notes

- The GeoLite2 database itself is licensed CC BY-SA 4.0 (see
  `LICENSE-GeoLite2.txt`). The attribution requirement is satisfied by
  shipping `LICENSE-GeoLite2.txt` alongside the database; if you ever
  surface the lookup result in a public-facing UI element, include the
  required attribution string visibly in the dashboard footer.
- The dashboard binary as a whole is **not** automatically CC BY-SA 4.0;
  the database is a discrete embedded data file with its own license,
  similar to how a project might embed an icon font with its own terms.
  This `LICENSE-GeoLite2.txt` notice is what keeps the two scopes
  separate.

## Why not call a hosted geolocation API instead?

- **No outbound network from the EC2** — the WireGuard host's egress is
  intentionally narrow (S3 + SSM + IMDS). Adding api.ipgeolocation.io or
  similar would require an SG rule, an IAM consideration if the request
  is signed, and a network-dependency on a third party.
- **No telemetry leak** — peer endpoint IPs would otherwise be sent to
  the geolocation provider on every dashboard refresh.
- **Predictable latency** — `getmmdb()` resolves in ~µs vs. a 50-200 ms
  network call.

The ~7 MB binary-size cost is the price of those properties. Acceptable
for a self-hosted single-instance VPN dashboard.
