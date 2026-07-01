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

  # Feature flag (spec 017): when true, a restapi_object resource drives the whole
  # peer set through the dashboard's PUT /api/clients bulk endpoint over the VPN.
  # Defaults OFF — flipping it on is an explicit, owner-opt-in commit. With it off
  # the restapi_object is count-gated to zero and this slice changes nothing on a box.
  manage_peers_via_api = false

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
