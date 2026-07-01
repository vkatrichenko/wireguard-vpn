terraform {
  backend "s3" {
    bucket       = "wireguard-vpn-test-tf-states"
    key          = "test/tf.tfstate"
    region       = "us-east-1"
    encrypt      = true
    use_lockfile = true
  }
}

provider "aws" {
  region = local.aws_region
  default_tags {
    tags = local.default_tags
  }
}

# Talks to the dashboard's bulk peer endpoint over the WireGuard tunnel. Reachable
# only from inside the VPN (the .1 server tunnel IP), so no auth headers here.
# `write_returns_object = true` because the dashboard's PUT /api/clients returns the
# canonical peer set in its response body, which the provider uses to refresh state.
# No `default_tags` — the restapi provider manages a non-AWS, non-taggable HTTP object.
provider "restapi" {
  uri                  = local.dashboard_base_url
  write_returns_object = true
}
