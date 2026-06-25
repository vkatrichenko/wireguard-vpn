# wireguard-vpn — Product Summary

**Vision:** A fully codified, version-controlled WireGuard VPN infrastructure that can be audited, reproduced, and extended by the community — plus a VPN-only dashboard to see its health and be told when something breaks.

**Target Audience:** DevOps engineers and privacy-conscious developers who want a self-hosted VPN without manual networking setup or opaque third-party services.

**Core Features:**

- **One-command VPN deploy** — Terraform modules that provision the full AWS stack (VPC, EC2, SG, IAM, WireGuard) in a single apply.
- **Multi-client support** — Configurable client list with unique keys and IP assignments.
- **VPN-only observability dashboard** — Single Go binary (tunnel-only): server/peer status, throughput, connection history, offline geo map, and per-client config download. No external requests, holds no client private keys.
- **Proactive alerting** — Watches service/disk/CPU/peer/transfer conditions and pushes to a Slack-compatible webhook (edge-trigger + cooldown + recovery); the webhook is manageable (set/test/revert) at runtime from the dashboard.

**Key Metrics:** Operational reliability (stable connectivity, minimal intervention) and operational visibility (see status at a glance; be alerted to failures in chat without SSH).
