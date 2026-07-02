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

  # --- Canonical client-list JSON (spec 018 §2.5) --------------------------
  # The SHARED, single-sourced serialization of the peer list used by BOTH the
  # S3 seed object (client_store.tf) and the root-module drift `check`. Slice 4's
  # Go serializer MUST reproduce this byte-for-byte or the drift check will report
  # phantom drift. Contract:
  #   - Array of objects, each with ONLY { name, address, public_key } — no
  #     `enabled`/disabled or any runtime state (so a UI enable/disable toggle is
  #     NOT git drift).
  #   - Sorted by `address` ascending, using a lexicographic string sort of the
  #     `address` value (HCL `sort()` semantics; the Go side must string-sort the
  #     same field). NOTE: this is a plain string sort, so "172.16.15.10/32" sorts
  #     before "172.16.15.6/32" — deterministic, and both sides must match it.
  #   - Encoded with `jsonencode` (compact, no extra whitespace). `jsonencode`
  #     emits object keys alphabetically, so each object serializes as
  #     {"address":…,"name":…,"public_key":…}.
  # Address is assumed unique per peer (each is a /32); the map keyed by address
  # is what gives us a stable, sortable ordering key.
  clients_by_address = {
    for c in var.clients_config : c.address => {
      name       = c.name
      address    = c.address
      public_key = c.public_key
    }
  }
  clients_canonical_json = jsonencode([
    for addr in sort(keys(local.clients_by_address)) : local.clients_by_address[addr]
  ])

  # The S3 client-list bridge exists ONLY in cloud mode. This flag gates every S3
  # resource (client_store.tf), the IAM grant (iam.tf), and the module outputs —
  # a default `local` apply provisions zero S3.
  client_store_enabled = var.client_management_mode == "cloud"

  # Store coordinates exported to the dashboard env. Non-empty ONLY in cloud mode
  # so local mode stays a clean no-op on the box (the dashboard sees empty coords
  # and never touches S3). Guarded [0] index: the resources are count-gated on the
  # same flag, so the index is only reached when they exist.
  client_store_s3_bucket = local.client_store_enabled ? aws_s3_bucket.client_list[0].bucket : ""
  client_store_s3_key    = local.client_store_enabled ? aws_s3_object.clients[0].key : ""

  # Resolved alert webhook URL (secret). Empty when no SSM param name is wired,
  # which suppresses the DASHBOARD_WEBHOOK_URL line in alerts.env. The count-gated
  # data source means we only touch SSM when the operator opts in.
  dashboard_webhook_url = var.dashboard_webhook_url_param != "" ? data.aws_ssm_parameter.dashboard_webhook_url[0].value : ""

  # Resolved opt-in transport secrets (spec 012), each empty unless its SSM param
  # name is wired — the count-gated data sources keep SSM untouched otherwise, and
  # an empty value suppresses its line in alerts.env (no behavior change when off).
  dashboard_slack_bot_token     = var.dashboard_slack_bot_token_param != "" ? data.aws_ssm_parameter.dashboard_slack_bot_token[0].value : ""
  dashboard_telegram_token      = var.dashboard_telegram_token_param != "" ? data.aws_ssm_parameter.dashboard_telegram_token[0].value : ""
  dashboard_discord_webhook_url = var.dashboard_discord_webhook_url_param != "" ? data.aws_ssm_parameter.dashboard_discord_webhook_url[0].value : ""
}

locals {
  user_data = templatefile("${path.module}/templates/user-data.txt", {
    # Shared portable installer, fetched at boot from raw GitHub at a pinned ref
    # (keeps user-data under EC2's 16 KB cap regardless of script growth). The
    # wrapper interpolates github_repo + install_script_ref into the curl URL.
    # github_repo is also the dashboard release source (same owner/name slug).
    github_repo        = var.github_repo
    install_script_ref = var.install_script_ref

    wg_server_private_key  = data.aws_ssm_parameter.wg_server_private_key.value
    wg_server_net          = var.wg_server_net
    wg_server_port         = var.wg_server_port
    peers                  = join("\n", local.wg_client_data_json)
    use_eip                = var.use_eip ? "enabled" : "disabled"
    eip_id                 = var.use_eip ? aws_eip.wireguard[0].id : null
    health_check_bucket    = aws_s3_bucket.health_check.bucket
    dashboard_release_tag  = var.dashboard_release_tag
    client_management_mode = var.client_management_mode
    client_store_s3_bucket = local.client_store_s3_bucket
    client_store_s3_key    = local.client_store_s3_key
    clients_json           = local.clients_json

    # Alert seed (spec 007/008 slice 5). Webhook is the secret; the rest are knobs.
    dashboard_webhook_url          = local.dashboard_webhook_url
    dashboard_host_label           = var.dashboard_alerts.host_label
    dashboard_alert_disk_pct       = var.dashboard_alerts.disk_pct
    dashboard_alert_cpu_pct        = var.dashboard_alerts.cpu_pct
    dashboard_alert_cpu_sustain    = var.dashboard_alerts.cpu_sustain
    dashboard_alert_transfer_bytes = var.dashboard_alerts.transfer_bytes

    # Opt-in multi-transport seed (spec 012). Tokens / Discord URL are secrets;
    # channel + chat-id are non-secret. Each empty value drops its alerts.env line.
    dashboard_slack_bot_token     = local.dashboard_slack_bot_token
    dashboard_slack_channel       = var.dashboard_slack_channel
    dashboard_telegram_token      = local.dashboard_telegram_token
    dashboard_telegram_chat_id    = var.dashboard_telegram_chat_id
    dashboard_discord_webhook_url = local.dashboard_discord_webhook_url
  })
}

# Per-architecture lookup. ami_suffix is the token in the Ubuntu AMI name
# ("amd64"/"arm64"); ami_arch is the value for the aws_ami `architecture` filter
# ("x86_64"/"arm64"); default_instance_type is the matching family.
locals {
  arch_config = {
    x86_64 = {
      ami_suffix            = "amd64"
      ami_arch              = "x86_64"
      default_instance_type = "t3a.micro"
    }
    arm64 = {
      ami_suffix            = "arm64"
      ami_arch              = "arm64"
      default_instance_type = "t4g.micro"
    }
  }

  # An explicit ami_id override wins; otherwise resolve from the arch-aware lookup.
  effective_ami_id = var.ami_id != null ? var.ami_id : data.aws_ami.ubuntu_2404[0].id

  # An explicit instance_type override wins; otherwise use the architecture default.
  effective_instance_type = var.instance_type != null ? var.instance_type : local.arch_config[var.cpu_architecture].default_instance_type
}

# turn the sg into a sorted list of string
locals {
  sg_wireguard_external = sort([aws_security_group.sg_wireguard_external.id])
}

# clean up and concat the above wireguard default sg with the additional_security_group_ids
locals {
  security_groups_ids = compact(concat(var.additional_security_group_ids, local.sg_wireguard_external))
}
