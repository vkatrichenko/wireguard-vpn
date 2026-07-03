# Functional Specification: Hardening & Server-Key Automation

- **Roadmap Item:** Hardening & server-key automation — remove the manual WireGuard server-key bootstrap, fix two dashboard client-management regressions, and close a batch of infrastructure security gaps.
- **Status:** Draft
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

This change bundles three small, independent workstreams surfaced by a code + infrastructure review. None is a new feature; all three reduce operator friction, close a data-loss path, or shrink the security blast radius. They ship together because each is low-risk and the whole set is "cleanup," but they touch three separate layers (dashboard, Terraform server-key flow, general infra posture) and can be verified independently.

**A — Dashboard client-management regressions.** Since spec 019 the dashboard UI is the sole authority for peers, but two read paths were never migrated off the old static Terraform boot manifest: a UI-added client's **detail** and **history** views 404, and the cloud-mode S3 backup can silently latch "offline" with no operator-visible signal, creating a stale-restore data-loss path. Both are live regressions for the now-primary workflow.

**B — Server private-key automation.** Today an operator must hand-create the WireGuard server private key in AWS SSM before the first apply, and Terraform then reads that key and threads it through the instance boot config. That places the private key in two at-rest locations it does not belong — the Terraform state file and the EC2 launch-template version — where a read-only permission (`s3:GetObject` on the state bucket, or `ec2:DescribeLaunchTemplateVersions`) is enough to recover it. The desired outcome: the operator never handles the server key, and the key never appears in Terraform state or the launch template. The instance manages its own key; the operator only ever sees the **public** key.

**C — Infrastructure security hardening.** A set of independent posture fixes (state-bucket encryption, instance metadata service v2, public SSH exposure, root-volume encryption, an under-configured helper bucket, and a few dead/no-op resources) each shrink exposure or remove misleading configuration. Individually minor; treated as one hardening pass.

**Success is measured by:** every affected workflow behaves correctly after the change (UI client detail/history work for UI-added clients; a rebuilt instance keeps the same server identity with zero operator key handling; the server private key is absent from state and launch template; the security scan surfaces no new HIGH/CRITICAL and the closed items no longer appear).

---

## 2. Functional Requirements (The "What")

### A. Dashboard client-management regressions

- **A1 — UI-added clients have working detail & history.** Client **detail** and **connection-history** views must resolve the requested client against the live dashboard client list (the same source the client table renders from), not the static Terraform boot manifest.
  - **Acceptance Criteria:**
    - [ ] Given a client added through the UI after boot, when the operator opens its **detail** panel, then the panel renders that client's data (no 404).
    - [ ] Given the same UI-added client, when the operator opens its **connection history**, then the history renders (no 404).
    - [ ] A client that exists only in the boot manifest but not in the live list is treated consistently with the rest of the UI (it is not specially resolvable via these two views).
    - [ ] Automated test coverage exists for a DB-only (UI-added) client reaching both views successfully.

- **A2 — The cloud-mode backup exposes its health and self-recovers.** When the S3 client-list backup becomes unreachable, the dashboard must (a) periodically re-check and recover on its own without a restart or a manual peer edit, and (b) make the backup's health observable to the operator.
  - **Acceptance Criteria:**
    - [ ] Given the backup was latched unreachable at boot, when the underlying access problem is fixed, then the dashboard recovers the backup on its own within one re-check interval (no service restart or peer mutation required).
    - [ ] The dashboard's health endpoint reports whether the client-store backup is ready, and this field is present only in cloud mode.
    - [ ] The dashboard surfaces a backup-health indicator in the UI (About view) showing OK vs. offline.
    - [ ] In local mode (no backup), none of the above adds noise — no offline signal, no health field.

### B. Server private-key automation

- **B1 — The instance owns its server key; the operator never handles it.** On boot the instance resolves its WireGuard server private key from SSM: if a real key is present it is reused; if absent (or a placeholder / not a valid key) the instance generates one and stores it. The operator performs no manual key creation step at any point.
  - **Acceptance Criteria:**
    - [ ] A fresh deploy comes up with a working WireGuard server with no operator-supplied private key.
    - [ ] Replacing/rebuilding the instance preserves the **same** server identity (existing client configs keep working) — the key is read back, not regenerated.
    - [ ] The manual "create the SSM parameter before first apply" step is removed from the setup docs and no longer required.

- **B2 — The private key is absent from Terraform state and the launch template.** After this change, the server private key value must not appear in the Terraform state file nor in any EC2 launch-template version.
  - **Acceptance Criteria:**
    - [ ] Inspecting Terraform state after an apply shows no server private-key value (only a non-secret placeholder for the parameter, if Terraform manages the parameter shell).
    - [ ] Inspecting the launch-template user-data shows no server private-key value.
    - [ ] A `terraform destroy` leaves no orphaned server-key parameter (clean teardown).

- **B3 — The operator can retrieve the server public key without connecting first.** The server **public** key is published where the operator can read it before the first VPN connection: in the installer's output (standalone path) and in a dedicated, non-secret SSM parameter (AWS path). The dashboard continues to display it once connected.
  - **Acceptance Criteria:**
    - [ ] After a standalone install, the server public key is printed in the installer output.
    - [ ] After an AWS deploy, the server public key is readable from a non-secret SSM parameter.
    - [ ] The published public key matches the key the running server actually uses.

- **B4 — The instance always has the IAM permissions it needs, independent of the Elastic IP toggle.** The instance's base role/profile and its baseline grants (Session Manager, client-store backup, and the new key parameters) must be present regardless of whether the Elastic IP feature is enabled.
  - **Acceptance Criteria:**
    - [ ] With the Elastic IP toggle **off**, the instance still has Session Manager access and the client-store backup grant.
    - [ ] Only the Elastic-IP-association permission is gated on the Elastic IP toggle.

### C. Infrastructure security hardening

- **C1 — Terraform state at rest requires a second gate.** The state bucket is encrypted such that read access to the object alone is insufficient to read its contents.
  - **Acceptance Criteria:** [ ] State bucket uses KMS-based encryption; a principal with only object-read but no key-decrypt permission cannot read state.

- **C2 — Instance metadata service requires v2.** The instance enforces IMDSv2 (token-required) with a minimal hop limit.
  - **Acceptance Criteria:** [ ] IMDSv1 is disabled on the instance; boot (which reads instance metadata) still succeeds.

- **C3 — SSH is removed; management is via Session Manager only.** Public SSH (port 22) is closed and the whole SSH key apparatus is deleted — the generated SSH keypair, the registered EC2 key pair, the SSM parameter holding the SSH private key, and the instance `key_name`. Operator shell access is via SSM Session Manager (IAM-gated, no key material, CloudTrail-audited), which the instance role already supports. This also removes the **second** private key from Terraform state (the SSH PEM), complementing B2.
  - **Acceptance Criteria:**
    - [ ] Port 22 is not reachable from `0.0.0.0/0` (the ingress rule is removed).
    - [ ] The operator can obtain a shell on the instance via SSM Session Manager (`aws ssm start-session`).
    - [ ] The SSH keypair, EC2 key pair, SSH-private-key SSM parameter, and instance `key_name` no longer exist; the SSH private key is absent from Terraform state.

- **C4 — The root volume is encrypted by configuration.** Root-EBS encryption is set explicitly, not left to the account default.
  - **Acceptance Criteria:** [ ] The instance's root volume is encrypted per the launch template.

- **C5 — The health-check bucket matches the client-list bucket's posture.** The helper health-check bucket has public-access-block, server-side encryption, and versioning.
  - **Acceptance Criteria:** [ ] The health-check bucket has all three protections.

- **C6 — Dead and misleading configuration is removed.** No-op and orphaned items are cleaned up: the ineffective `ignore_changes` on the instance's inline user-data; the unused world-open security group in the network module; the bare `NoSuchKey`/`404` string match tightened to the proper error code; the unused error sentinel; and the state bucket's object-lock flag either gains a retention rule or is dropped so it no longer implies unavailable protection.
  - **Acceptance Criteria:**
    - [ ] The no-op `ignore_changes` on inline user-data is removed.
    - [ ] The orphaned world-open security group is removed (or wired up).
    - [ ] The backup "object absent" classification keys on the proper error code, not a bare substring.
    - [ ] The unused error sentinel is removed.
    - [ ] The state bucket no longer advertises object-lock protection it does not enforce.

---

## 3. Scope and Boundaries

### In-Scope

- The three workstreams (A, B, C) above, across the dashboard (Go), the Terraform `wireguard` and `network/vpc` modules, the `dev` root and its state-bucket bootstrap, and the shared `install.sh` / AWS user-data wrapper.
- A dashboard release cut for the A-workstream changes.
- Documentation updates: removing the manual server-key bootstrap step and documenting where the public key is published.

### Out-of-Scope

- Any change to the standalone-VPS key behavior beyond what already exists — `install.sh` stays cloud-agnostic; the SSM key resolution is AWS-wrapper-only.
- Server-key **rotation** tooling (a deliberate `rotate` command / workflow) — the design preserves a stable identity; rotation is a separate concern.
- Providing a bastion, VPN-gated SSH, or any replacement remote-shell path beyond SSM Session Manager (Session Manager is the sole management access after C3).
- Multi-environment / second root module work.
- Any peer-management behavior change — spec 019's UI-sole-authority model is unchanged; this spec only fixes its two broken read paths and its backup observability.
- Public/non-VPN dashboard exposure, TLS, auth — unchanged (dashboard stays VPN-only).
- All other roadmap items remain out-of-scope and will be handled in their own specifications.
