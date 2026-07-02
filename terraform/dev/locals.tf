locals {
  aws_region   = "us-east-1"
  project_name = "wireguard-vpn"
  environment  = "test"

  vpc_name = "${local.project_name}-vpc"
  vpc_cidr = "10.23.0.0/16"

  # Client-management mode (spec 018): "local" = dashboard/SQLite-managed peers
  # (default, no instance churn); "cloud" = S3-bridged peers (later slices).
  client_management_mode = "cloud"

  # Canonical peer set — the first-boot seed passed to the wireguard module's
  # clients_config input.
  clients_config = [
    {
      name       = "laptop"
      address    = "172.16.15.2/32"
      public_key = "OYR4niUZ/Ay5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4="
    },
    {
      name       = "test1"
      address    = "172.16.15.7/32"
      public_key = "WuF+51NTLZllDf1U5RSdtPT5xUVuezwCm9ypuOy22io="
    },
  ]

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }
}
