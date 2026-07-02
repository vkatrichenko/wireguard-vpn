locals {
  aws_region   = "us-east-1"
  project_name = "wireguard-vpn"
  environment  = "test"

  vpc_name = "${local.project_name}-vpc"
  vpc_cidr = "10.23.0.0/16"

  # Client-management mode (spec 019): "local" = dashboard/SQLite-managed peers
  # (no S3); "cloud" = SQLite + S3 as a pure durable backup (Terraform never
  # reads it, no drift detection).
  client_management_mode = "cloud"

  # Single admin bootstrap peer (spec 019) — the ONLY peer Terraform seeds, for
  # anti-lockout. Day-to-day peers are managed entirely in the dashboard UI, not
  # here. An object { name, public_key } seeds an admin; `null` seeds no peer
  # (empty WG peer set + empty clients.json), which is a valid deploy.
  admin_peer = {
    name       = "laptop"
    public_key = "OYR4niUZ/Ay5KAxwyvfAVOjgKo4NwQb0wRSyqRblPF4="
  }

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }
}
