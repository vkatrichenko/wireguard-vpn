locals {
  wg_client_data_json = [
    for client in var.clients_config : templatefile("${path.module}/templates/client-data.tpl", {
      client_pub_key       = values(client)[0]
      client_ip            = keys(client)[0]
      persistent_keepalive = var.wg_persistent_keepalive
    })
  ]
}

locals {
  user_data = templatefile("${path.module}/templates/user-data.txt", {
    wg_server_private_key          = data.aws_ssm_parameter.wg_server_private_key.value
    wg_server_net                  = var.wg_server_net
    wg_server_port                 = var.wg_server_port
    peers                          = join("\n", local.wg_client_data_json)
    use_eip                        = var.use_eip ? "enabled" : "disabled"
    eip_id                         = var.use_eip ? aws_eip.wireguard[0].id : null
    health_check_bucket            = aws_s3_bucket.health_check.bucket
    dashboard_artifact_bucket_name = var.dashboard_artifact_bucket_name == null ? "" : var.dashboard_artifact_bucket_name
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
