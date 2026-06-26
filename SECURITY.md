# Security Policy

Thanks for helping keep `wireguard-vpn` and its users safe. This is an early,
solo-maintained project; the process below is intentionally lightweight but takes
security reports seriously.

## Reporting a vulnerability

**Please report security vulnerabilities privately — do not open a public issue or
pull request for a security problem.** Public disclosure before a fix is available
puts every operator running this VPN at risk.

To report a vulnerability, use GitHub's **private security advisories**:

1. Go to the repository's **Security** tab.
2. Open **Advisories**.
3. Click **Report a vulnerability**.

This opens a private channel visible only to you and the maintainer. No public
email address is required or exposed.

Please include, where you can:

- A description of the issue and its potential impact.
- Steps to reproduce, or a proof of concept.
- The affected component (Terraform infrastructure or the Go dashboard) and any
  relevant version/commit.

### Operator prerequisite

The "Report a vulnerability" button only appears once the maintainer has enabled
**Private vulnerability reporting** for the repository under
**Settings → Code security and analysis**. If you do not see the button, the
feature has not yet been turned on — please reach out through the repository's
contact channels and ask the maintainer to enable it.

## Supported versions

This is an early-stage, single-maintainer project. Only the latest `main` is
supported; fixes land on `main` and there are no backports to older commits or tags.

| Version | Supported |
|---------|-----------|
| `main` (latest) | Yes |
| Anything older | No |

## Response expectations

As a best effort, the maintainer aims to acknowledge a report within roughly
**5 business days**. Given solo maintenance, there is **no formal SLA** for triage
or fix timelines — please treat the acknowledgement target as a goal, not a guarantee.

## Scope

In scope:

- The Terraform infrastructure under `terraform/` (the WireGuard EC2 server, VPC,
  IAM, security groups, and related AWS resources).
- The Go dashboard under `dashboard/`.

Out of scope: third-party dependencies and services (report those upstream),
and issues that require an attacker to already have privileged access to the
operator's AWS account or host.
