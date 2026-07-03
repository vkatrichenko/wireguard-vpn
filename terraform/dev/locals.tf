locals {
  aws_region   = "us-east-1"
  project_name = "wireguard-vpn"
  environment  = "test"

  vpc_name = "${local.project_name}-vpc"
  vpc_cidr = "10.23.0.0/16"

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }
}
