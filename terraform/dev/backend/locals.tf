locals {
  aws_region = "us-east-1"

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }

  state_bucket = "wireguard-vpn-test-tf-states"
}
