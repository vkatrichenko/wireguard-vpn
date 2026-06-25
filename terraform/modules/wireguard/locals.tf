locals {
  wg_client_data_json = [
    for client in var.clients_config : templatefile("${path.module}/templates/client-data.tpl", {
      client_name          = client.name
      client_pub_key       = client.public_key
      client_ip            = client.address
      persistent_keepalive = var.wg_persistent_keepalive
    })
  ]

  # JSON manifest consumed by the dashboard at runtime
  # (path: /etc/wireguard-dashboard/clients.json — written by user-data).
  # Stock WireGuard has no native concept of peer names; this file is the
  # source of truth that maps peer public keys to human-readable labels.
  clients_json = jsonencode([
    for c in var.clients_config : {
      name       = c.name
      address    = c.address
      public_key = c.public_key
    }
  ])

  # Resolved alert webhook URL (secret). Empty when no SSM param name is wired,
  # which suppresses the DASHBOARD_WEBHOOK_URL line in alerts.env. The count-gated
  # data source means we only touch SSM when the operator opts in.
  dashboard_webhook_url = var.dashboard_webhook_url_param != "" ? data.aws_ssm_parameter.dashboard_webhook_url[0].value : ""
}

locals {
  user_data = templatefile("${path.module}/templates/user-data.txt", {
    wg_server_private_key  = data.aws_ssm_parameter.wg_server_private_key.value
    wg_server_net          = var.wg_server_net
    wg_server_port         = var.wg_server_port
    peers                  = join("\n", local.wg_client_data_json)
    use_eip                = var.use_eip ? "enabled" : "disabled"
    eip_id                 = var.use_eip ? aws_eip.wireguard[0].id : null
    health_check_bucket    = aws_s3_bucket.health_check.bucket
    dashboard_release_tag  = var.dashboard_release_tag
    dashboard_release_repo = var.dashboard_release_repo
    clients_json           = local.clients_json

    # Alert seed (spec 007/008 slice 5). Webhook is the secret; the rest are knobs.
    dashboard_webhook_url          = local.dashboard_webhook_url
    dashboard_host_label           = var.dashboard_alerts.host_label
    dashboard_alert_disk_pct       = var.dashboard_alerts.disk_pct
    dashboard_alert_cpu_pct        = var.dashboard_alerts.cpu_pct
    dashboard_alert_cpu_sustain    = var.dashboard_alerts.cpu_sustain
    dashboard_alert_peer_stale     = var.dashboard_alerts.peer_stale
    dashboard_alert_transfer_bytes = var.dashboard_alerts.transfer_bytes
  })
}

# turn the sg into a sorted list of string
locals {
  sg_wireguard_external = sort([aws_security_group.sg_wireguard_external.id])
}

# clean up and concat the above wireguard default sg with the additional_security_group_ids
locals {
  security_groups_ids = compact(concat(var.additional_security_group_ids, local.sg_wireguard_external))
}
