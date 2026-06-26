---
name: Bug report
about: Report a problem with the WireGuard VPN infrastructure, the tunnel, or the dashboard
title: "[Bug]: "
labels: bug
---

<!--
Thanks for filing a bug. Please fill in the sections below.
Before posting: REDACT any secrets, private keys, public IPs, or client
allowed-IPs from logs and config you paste here.
-->

## Summary / what happened

<!-- A clear, one-or-two sentence description of the problem. -->

## Steps to reproduce

1.
2.
3.

## Expected behavior

<!-- What you expected to happen. -->

## Actual behavior

<!-- What actually happened. Include error messages verbatim where possible. -->

## Affected component

<!-- Check all that apply. -->

- [ ] Terraform infrastructure (VPC, EC2, IAM, SSM, etc.)
- [ ] WireGuard tunnel (peer connectivity, `wg0`, handshakes, routing)
- [ ] Dashboard (Go app / web UI)
- [ ] Other / not sure

## Environment

- **Terraform version:** <!-- e.g. 1.14.8 (the repo pins = 1.14.8) -->
- **AWS region / profile:** <!-- e.g. us-east-1, AWS_PROFILE=csm -->
- **Dashboard release tag (`dashboard_release_tag`):** <!-- e.g. v0.0.5, or "" if the dashboard is disabled -->
- **Client OS / WireGuard client:** <!-- e.g. macOS 15 / WireGuard 1.0.16, iOS app, wg-quick on Linux -->

## Relevant logs

<!--
Pull the relevant logs and paste them here. Useful sources:

- Instance bootstrap / user-data:   /var/log/cloud-init-output.log  (on the EC2 instance)
- Dashboard service:                journalctl -u wireguard-dashboard
- WireGuard tunnel:                 journalctl -u wg-quick@wg0  (and `wg show`)

REMINDER: redact private/public keys, peer public keys, real public IPs,
and client allowed-IPs before pasting.
-->

```
<paste redacted logs here>
```

## Additional context

<!-- Anything else that helps — recent changes, when it started, screenshots, etc. -->
