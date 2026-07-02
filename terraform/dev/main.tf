module "network" {
  source       = "../modules/network/vpc"
  project_name = local.project_name
  env          = local.environment
  vpc_name     = local.vpc_name
  vpc_cidr     = local.vpc_cidr
  tags         = local.default_tags
  ports        = [22]
}

module "wireguard" {
  source = "../modules/wireguard"

  project_name = local.project_name
  env          = local.environment


  vpc_id                      = module.network.vpc_id
  subnet_id                   = module.network.public_subnets[0]
  wg_server_net               = "172.16.15.1/24"
  wg_server_private_key_param = "/config/${local.project_name}-${local.environment}/default-private-key"
  # $ wg genkey | tee privatekey | wg pubkey > publickey
  # [Interface]
  # PrivateKey = // user private key
  # ListenPort = 51820
  # Address = 10.22.123.13/32 //change subnet
  # DNS = 10.22.0.2

  # [Peer]
  # PublicKey = wireguard part, DO NOT CHANGE
  # AllowedIPs = 10.22.0.0/16
  # Endpoint = 54.245.26.247:51820

  client_management_mode = local.client_management_mode

  # The boot seed is ALWAYS the full canonical peer set — in BOTH local and cloud
  # modes (spec 018). Peer-management behavior is selected by
  # `client_management_mode`, NOT by emptying this seed:
  #   local mode → this seed only bootstraps a fresh box; peers are then managed
  #     live in the dashboard UI backed by instance-local SQLite, no instance churn.
  #   cloud mode → this seed one-time-bootstraps the S3-bridged peer object; the
  #     dashboard then owns it and Terraform only warns on drift (later slices).
  # Seeding the full set unconditionally is what kills spec-017's zero-peer
  # cold-start lockout.
  clients_config = local.clients_config
  # additional_security_group_ids = [
  #   module.development_custom_security_groups["dev_SELF"].security_group_id
  # ]

  # set its NAME here — Terraform reads it at apply and seeds DASHBOARD_WEBHOOK_URL
  # into /etc/wireguard-dashboard/alerts.env. Empty = no webhook line written.
  dashboard_webhook_url_param = ""
  # Opt-in multi-transport secrets (spec 012), same SSM-name pattern as the
  # webhook above — all default "" so every transport stays disabled until wired:
  #   dashboard_slack_bot_token_param / dashboard_slack_channel
  #   dashboard_telegram_token_param  / dashboard_telegram_chat_id
  #   dashboard_discord_webhook_url_param
  # dashboard_alerts intentionally omitted — defaults apply (disk/cpu 90%, etc.).

  tags = local.default_tags
}
