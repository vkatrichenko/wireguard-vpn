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

  dashboard_release_tag = "v0.0.15"
  github_repo           = "vkatrichenko/wireguard-vpn"

  # Single admin bootstrap peer (spec 019) — the ONLY peer Terraform seeds, for
  # anti-lockout. The dashboard UI is the sole authority for every other peer, so
  # editing the peer list no longer churns the instance. `null` seeds no peer.
  admin_peer = local.admin_peer
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
