locals {
  aws_region   = "us-east-1"
  project_name = "wireguard-vpn"
  environment  = "test"

  vpc_name = "${local.project_name}-vpc"
  vpc_cidr = "10.23.0.0/16"

  # Pinned dashboard release — the single source of truth for the running
  # dashboard version (same explicit-and-reviewable philosophy as the pinned
  # AMI). Bumping the tag re-renders user-data, rolls a new launch-template
  # version, and replaces the instance. The binary is pulled at boot from the
  # public GitHub Release and SHA256-verified; requires the repo to be public.

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }
}
