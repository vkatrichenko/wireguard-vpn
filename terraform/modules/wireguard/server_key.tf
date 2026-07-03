# --- WireGuard server key SSM parameters (spec 020 slice 3) -----------------
# The server's WireGuard identity is split by secrecy:
#
#   * PRIVATE key — INSTANCE-OWNED, deliberately NOT declared in Terraform. The
#     boot wrapper creates/reads it directly in SSM (GetParameter, and
#     PutParameter to seed it on a fresh box); an existing operator-created param
#     is adopted as-is (no import, no rotation, Terraform never touches it). This
#     is the whole point of the design: because Terraform never manages the param,
#     the secret value NEVER lands in Terraform state. `aws_ssm_parameter` always
#     stores `value` in state as plaintext (and would read the real value back on
#     every refresh even behind `ignore_changes`), so the only way to keep the key
#     out of state is to not declare it — hence there is no resource here for it,
#     only the name (local.wg_server_private_key_param_name) and a constructed ARN
#     (local.wg_server_private_key_arn) for the IAM grant.
#
#   * PUBLIC key — TF-MANAGED SHELL. Non-secret, so Terraform owning it is fine
#     and buys us a plan-time ARN (for the IAM grant), a clean destroy, and a
#     stable, declared read location. Terraform only ever writes the
#     "UNINITIALIZED" sentinel; the instance derives the real public key at boot
#     and overwrites it via PutParameter. `ignore_changes = [value]` stops
#     Terraform from resetting it back to the sentinel on subsequent applies.
#
# The param is UNCONDITIONAL — created on every AWS deploy — because the server
# key is required in both `local` and `cloud` client-management modes.
#
# key_id is intentionally NOT set: the value is a public key, so the default
# String type (unencrypted) is appropriate; no KMS key involved.
resource "aws_ssm_parameter" "wg_server_public_key" {
  name        = "/config/${var.project_name}-${var.env}/server-public-key"
  description = "Terraform Managed shell (spec 020). WireGuard server PUBLIC key derived on the instance. Terraform only ever writes the 'UNINITIALIZED' sentinel; the instance writes the real public key at boot via PutParameter. ignore_changes keeps the real value invisible to Terraform."
  type        = "String"
  value       = "UNINITIALIZED"

  tags = var.tags

  lifecycle {
    ignore_changes = [value]
  }
}
