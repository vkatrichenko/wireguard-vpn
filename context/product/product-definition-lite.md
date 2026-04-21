# wireguard-vpn — Product Summary

**Vision:** A fully codified, version-controlled WireGuard VPN infrastructure that can be audited, reproduced, and extended by the community.

**Target Audience:** DevOps engineers and privacy-conscious developers who want a self-hosted VPN without manual networking setup or opaque third-party services.

**Core Features:**

- **One-command VPN deploy** — Terraform modules that provision the full AWS stack (VPC, EC2, SG, IAM, WireGuard) in a single apply.
- **Multi-client support** — Configurable client list with unique keys and IP assignments.

**Key Metric:** Operational reliability — stable connectivity with minimal manual intervention.
