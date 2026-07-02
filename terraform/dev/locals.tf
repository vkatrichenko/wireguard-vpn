locals {
  aws_region   = "us-east-1"
  project_name = "wireguard-vpn"
  environment  = "test"

  vpc_name = "${local.project_name}-vpc"
  vpc_cidr = "10.23.0.0/16"

  # WireGuard server tunnel address (CIDR). Single source of truth: consumed both
  # by the wireguard module (wg_server_net input) and by the dashboard URL below.
  wg_server_net = "172.16.15.1/24"

  # The dashboard listens on the server tunnel IP (.1) + port 8080, reachable only
  # over the VPN. Derived from wg_server_net so the host stays in one place.
  dashboard_base_url = "http://${split("/", local.wg_server_net)[0]}:8080"

  # Client-management mode (spec 018) — selects the peer-management path. Consumed by
  # the `wireguard` module input added in Slice 3; validated at the module boundary.
  #   "local" (default) → peers are managed live in the dashboard UI (spec 015).
  #     `clients_config` is only the first-boot seed; peer edits happen in the UI and
  #     cause NO instance churn.
  #   "cloud"           → peers are declared here in `clients_config`, delivered via
  #     user-data, and editing them auto-replaces the instance (create-before-destroy,
  #     EIP + server key preserved). Wired in the module in Slice 3.
  client_management_mode = "local"

  # spec-017's live `PUT /api/clients` restapi path (restapi_object below), kept
  # DORMANT and INDEPENDENT of `client_management_mode`. Experimental, off by default.
  # Do NOT combine with `cloud` mode — that would give two owners of the peer set.
  enable_restapi_peer_sync = false

  # Canonical peer set — single source of truth for BOTH the boot seed (module
  # clients_config input) and the API-managed set (restapi_object data below).
  clients_config = [
    {
      name       = "vkatrychenko"
      address    = "172.16.15.6/32"
      public_key = "OVtCVOCizGvTVq2vhlymbEOmVnzfZaQKxXgUk+5eYwM="
    },
    {
      name       = "test1"
      address    = "172.16.15.7/32"
      public_key = "WuF+51NTLZllDf1U5RSdtPT5xUVuezwCm9ypuOy22io="
    },
  ]

  # Deterministic ordering by `address` (ASC). MUST match the dashboard's
  # GET /api/clients/export?format=tfvars, which is `ORDER BY address ASC`, so the
  # provider's read-back diff compares like-for-like and doesn't report phantom drift.
  clients_sorted = [
    for addr in sort([for c in local.clients_config : c.address]) :
    [for c in local.clients_config : c if c.address == addr][0]
  ]

  default_tags = {
    "Managed"     = "by-terraform"
    "Environment" = "test"
    "Project"     = "wireguard-vpn-test"
    "Owner"       = "Vladyslav Katrychenko"
  }
}
