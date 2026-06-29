data "aws_ssm_parameter" "wg_server_private_key" {
  name = var.wg_server_private_key_param
}

# Ubuntu 24.04 (Noble) AMI lookup, resolved from cpu_architecture. Only queried
# when no explicit ami_id override is set — the override short-circuits the lookup.
data "aws_ami" "ubuntu_2404" {
  count = var.ami_id == null ? 1 : 0

  most_recent = true
  owners      = ["099720109477"] # Canonical

  filter {
    name   = "name"
    values = ["ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-${local.arch_config[var.cpu_architecture].ami_suffix}-server-*"]
  }

  filter {
    name   = "virtualization-type"
    values = ["hvm"]
  }

  filter {
    name   = "architecture"
    values = [local.arch_config[var.cpu_architecture].ami_arch]
  }

  filter {
    name   = "root-device-type"
    values = ["ebs"]
  }
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

# Opt-in multi-transport secrets (spec 012) — same posture as the webhook above:
# read only when the operator wires a parameter name, with the operator's apply
# creds (no instance IAM grant), and seeded into user-data. .value is sensitive,
# so none of these appear in plan output.
data "aws_ssm_parameter" "dashboard_slack_bot_token" {
  count = var.dashboard_slack_bot_token_param != "" ? 1 : 0

  name            = var.dashboard_slack_bot_token_param
  with_decryption = true
}

data "aws_ssm_parameter" "dashboard_telegram_token" {
  count = var.dashboard_telegram_token_param != "" ? 1 : 0

  name            = var.dashboard_telegram_token_param
  with_decryption = true
}

data "aws_ssm_parameter" "dashboard_discord_webhook_url" {
  count = var.dashboard_discord_webhook_url_param != "" ? 1 : 0

  name            = var.dashboard_discord_webhook_url_param
  with_decryption = true
}
