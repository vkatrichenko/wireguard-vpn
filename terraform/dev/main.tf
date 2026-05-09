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

  project_name                = local.project_name
  env                         = local.environment
  ami_id                      = data.aws_ami.ubuntu_2404.id # Ubuntu Server 24.04 x86
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
    # {
    #   name       = "<peer-name>"
    #   address    = "10.222.123.9/32"
    #   public_key = "<peer-public-key>"
    # },
  ]
  # additional_security_group_ids = [
  #   module.development_custom_security_groups["dev_SELF"].security_group_id
  # ]

  dashboard_artifact_bucket_arn  = module.dashboard.bucket_arn
  dashboard_artifact_bucket_name = module.dashboard.bucket_name

  tags = local.default_tags
}

module "dashboard" {
  source = "../modules/dashboard"

  project_name        = local.project_name
  env                 = local.environment
  target_instance_arn = module.wireguard.instance_arn
  tags                = local.default_tags
}

module "github_oidc" {
  source = "../modules/github-oidc"

  use_existing = true

  roles = {
    "dashboard-ci-build" = {
      name_suffix = "dashboard-ci-build"
      subject     = "repo:vkatrichenko/wireguard-vpn:ref:refs/heads/main"
      s3_put_object = [
        {
          bucket_arn = module.dashboard.bucket_arn
          prefixes   = ["latest/*", "main-*/*"]
        },
      ]
    }
    "dashboard-ci-deploy" = {
      name_suffix        = "dashboard-ci-deploy"
      subject            = "repo:vkatrichenko/wireguard-vpn:ref:refs/heads/main"
      inline_policy_json = module.dashboard.deploy_policy_json
    }
  }

  tags = local.default_tags
}
