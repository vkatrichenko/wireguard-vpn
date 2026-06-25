data "aws_ssm_parameter" "wg_server_private_key" {
  name = var.wg_server_private_key_param
}

# Alert webhook URL — read only when the operator has wired a parameter name.
# Same posture as wg_server_private_key: Terraform reads SSM at apply with the
# operator's creds (no instance IAM grant) and seeds the value into user-data.
# .value is always sensitive, so it never appears in plan output.
data "aws_ssm_parameter" "dashboard_webhook_url" {
  count = var.dashboard_webhook_url_param != "" ? 1 : 0

  name            = var.dashboard_webhook_url_param
  with_decryption = true
}
