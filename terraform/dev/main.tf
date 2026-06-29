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

  clients_config = [
    {
      name       = "vkatrychenko"
      address    = "172.16.15.6/32"
      public_key = "OVtCVOCizGvTVq2vhlymbEOmVnzfZaQKxXgUk+5eYwM="
    },
    {
      name       = "test"
      address    = "172.16.15.7/32"
      public_key = "pe3YV5lUvF01ow559PXk7W+mLsuGggRh6v57J1ywKU0="
    },
  ]
  # additional_security_group_ids = [
  #   module.development_custom_security_groups["dev_SELF"].security_group_id
  # ]
  dashboard_release_tag  = "v0.0.7"
  dashboard_release_repo = "vkatrichenko/wireguard-vpn"

  # Portable installer fetched at boot from raw GitHub, SHA256-verified before it
  # runs (spec 014). The repo MUST be public for the anonymous raw fetch to
  # resolve — a private repo 404s and aborts the boot (no `.ready`).
  install_script_repo   = "vkatrichenko/wireguard-vpn"
  install_script_sha256 = "7be62a73b20c8845c8b9fd72c79f6d77b8ca6a2bb6e3d545e54ae5d67befd2cf"
  # Pinned to the public main commit that carries this exact scripts/install.sh
  # (verified: raw fetch returns HTTP 200 and matches install_script_sha256). When
  # install.sh changes, push it, then bump BOTH this ref and the sha256 above
  # (`sha256sum scripts/install.sh`) in one reviewable commit.
  # install_script_ref = "5d05d6a4ba53fd6ff1dad923fa857a3b866461f5"

  # Alert seed (spec 008 slice 5), wired-but-disabled. To enable: create the SSM
  # parameter out-of-band (e.g. `aws ssm put-parameter --type SecureString`) and
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

# The dashboard binary is now distributed as a public GitHub Release (spec 005):
# the instance pulls a pinned tag at boot over HTTPS with SHA256 verification.
# The old private path — the S3 artifact bucket (`modules/dashboard`), the SSM
# deploy document, and the GitHub-OIDC CI build/deploy roles (`modules/github-oidc`
# wiring) — is intentionally gone. The release workflow authenticates only with
# GITHUB_TOKEN, so no AWS-facing CI role remains to wire here.
