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
  wg_server_net               = local.wg_server_net
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

  # The boot seed is ALWAYS the full canonical, address-sorted peer set — in BOTH
  # local and cloud modes (spec 018). Peer-management behavior is selected by
  # `client_management_mode` (threaded to the module in Slice 3), NOT by emptying
  # this seed:
  #   local mode → this seed only bootstraps a fresh box; peers are then managed
  #     live in the dashboard UI with no instance churn.
  #   cloud mode → this seed is the declared source of truth; editing it auto-
  #     replaces the instance (wired in the module, Slice 3).
  # Seeding the full set unconditionally is what kills spec-017's zero-peer
  # cold-start lockout.
  clients_config = local.clients_sorted
  # additional_security_group_ids = [
  #   module.development_custom_security_groups["dev_SELF"].security_group_id
  # ]
  dashboard_release_tag = "v0.0.12"

  # Single GitHub owner/name slug used BOTH for the raw scripts/install.sh fetch
  # and the dashboard release download (spec 014). The repo MUST be public for the
  # anonymous raw fetch / release download to resolve — a private repo 404s and
  # aborts the boot (no `.ready`). No checksum: the portable installer is fetched
  # at boot from github_repo at install_script_ref (module default `main`).
  github_repo = "vkatrichenko/wireguard-vpn"
  # install_script_ref pins the exact scripts/install.sh commit/tag fetched at
  # boot. Left commented so the module default `main` applies; uncomment and pin
  # to a commit SHA to freeze the installer version.
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

# Terraform-managed peer set (spec 017, demoted by spec 018). Drives the dashboard's
# PUT /api/clients bulk endpoint over the VPN, making the whole client list a single
# reconciled object rather than a boot-only seed. Now gated on the INDEPENDENT,
# OFF-by-default `enable_restapi_peer_sync` flag — this is a DORMANT spec-017 path,
# NOT part of either client_management_mode's normal flow. Do NOT combine it with
# `cloud` mode: both would own the peer set. Inert until the owner opts in.
#
# This is a SINGLETON collection, not a per-id object: read/update/destroy paths
# are overridden to hit /api/clients directly (no `/{id}` suffix the provider would
# otherwise append). `object_id` is a static "managed" so the address is stable.
# Read hits the export endpoint (?format=tfvars, address-sorted) to match state.
# Destroy PUTs an empty clients_config so `terraform destroy` clears peers cleanly.
resource "restapi_object" "peers" {
  count = local.enable_restapi_peer_sync ? 1 : 0

  path      = "/api/clients"
  object_id = "managed"
  data      = jsonencode({ clients_config = local.clients_sorted })

  create_method = "PUT"
  create_path   = "/api/clients"

  update_method = "PUT"
  update_path   = "/api/clients"

  read_method  = "GET"
  read_path    = "/api/clients/export"
  query_string = "format=tfvars"

  destroy_method = "PUT"
  destroy_path   = "/api/clients"
  destroy_data   = jsonencode({ clients_config = [] })

  depends_on = [module.wireguard]
}

# The dashboard binary is now distributed as a public GitHub Release (spec 005):
# the instance pulls a pinned tag at boot over HTTPS with SHA256 verification.
# The old private path — the S3 artifact bucket (`modules/dashboard`), the SSM
# deploy document, and the GitHub-OIDC CI build/deploy roles (`modules/github-oidc`
# wiring) — is intentionally gone. The release workflow authenticates only with
# GITHUB_TOKEN, so no AWS-facing CI role remains to wire here.
