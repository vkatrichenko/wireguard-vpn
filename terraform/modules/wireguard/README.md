# wireguard

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | = 1.14.8 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | = 6.41.0 |
| <a name="requirement_null"></a> [null](#requirement\_null) | = 3.2.4 |
| <a name="requirement_tls"></a> [tls](#requirement\_tls) | = 4.0.3 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | = 6.41.0 |
| <a name="provider_null"></a> [null](#provider\_null) | = 3.2.4 |
| <a name="provider_tls"></a> [tls](#provider\_tls) | = 4.0.3 |

## Modules

No modules.

## Resources

| Name | Type |
|------|------|
| [aws_eip.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/eip) | resource |
| [aws_eip_association.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/eip_association) | resource |
| [aws_iam_instance_profile.wireguard_profile](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_instance_profile) | resource |
| [aws_iam_policy.wireguard_policy](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_policy) | resource |
| [aws_iam_role.wireguard_role](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_role) | resource |
| [aws_iam_role_policy_attachment.wireguard_roleattach](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_role_policy_attachment) | resource |
| [aws_iam_role_policy_attachment.wireguard_ssm_core](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_role_policy_attachment) | resource |
| [aws_instance.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/instance) | resource |
| [aws_key_pair.ssh](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/key_pair) | resource |
| [aws_launch_template.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/launch_template) | resource |
| [aws_s3_bucket.health_check](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket) | resource |
| [aws_security_group.sg_wireguard_external](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/security_group) | resource |
| [aws_ssm_parameter.ssh_private_key](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/ssm_parameter) | resource |
| [null_resource.status_check](https://registry.terraform.io/providers/hashicorp/null/3.2.4/docs/resources/resource) | resource |
| [tls_private_key.ssh](https://registry.terraform.io/providers/hashicorp/tls/4.0.3/docs/resources/private_key) | resource |
| [aws_ami.ubuntu_2404](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ami) | data source |
| [aws_iam_policy_document.ec2_assume_role](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.wireguard_policy_doc](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |
| [aws_ssm_parameter.dashboard_discord_webhook_url](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |
| [aws_ssm_parameter.dashboard_slack_bot_token](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |
| [aws_ssm_parameter.dashboard_telegram_token](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |
| [aws_ssm_parameter.dashboard_webhook_url](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |
| [aws_ssm_parameter.wg_server_private_key](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_additional_security_group_ids"></a> [additional\_security\_group\_ids](#input\_additional\_security\_group\_ids) | Additional security groups if provided, default empty. | `list(string)` | <pre>[<br/>  ""<br/>]</pre> | no |
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | Optional explicit AMI override for the WG server. When set (non-null), it takes precedence over the cpu\_architecture-derived AMI lookup. When null (default), the module resolves the latest Ubuntu 24.04 AMI for the selected cpu\_architecture via the aws\_ami data source. | `string` | `null` | no |
| <a name="input_clients_config"></a> [clients\_config](#input\_clients\_config) | List of WireGuard peer (client) definitions. Each entry's `name` is rendered into /etc/wireguard-dashboard/clients.json by user-data so the dashboard can label peers; `address` is the CIDR the peer is allowed inside the WG subnet (e.g. "172.16.15.6/32"); `public_key` is the peer's WireGuard public key. | <pre>list(object({<br/>    name       = string<br/>    address    = string<br/>    public_key = string<br/>  }))</pre> | n/a | yes |
| <a name="input_cpu_architecture"></a> [cpu\_architecture](#input\_cpu\_architecture) | CPU architecture for the WireGuard server. Drives the Ubuntu AMI name suffix, the AMI `architecture` filter, and the default instance type. Allowed: "x86\_64" (amd64, t3a.micro default) or "arm64" (Graviton, t4g.micro default). Default "arm64" for better price/performance. | `string` | `"arm64"` | no |
| <a name="input_dashboard_alerts"></a> [dashboard\_alerts](#input\_dashboard\_alerts) | Spec-007 alert thresholds seeded into /etc/wireguard-dashboard/alerts.env (mapped to DASHBOARD\_HOST\_LABEL / DASHBOARD\_ALERT\_DISK\_PCT / \_CPU\_PCT / \_CPU\_SUSTAIN / \_TRANSFER\_BYTES). host\_label empty (default) omits DASHBOARD\_HOST\_LABEL so the Go side falls back to os.Hostname(). cpu\_sustain is a Go duration (e.g. "5m"); transfer\_bytes is a humanized size (e.g. "50GiB"). | <pre>object({<br/>    host_label     = optional(string, "")<br/>    disk_pct       = optional(number, 90)<br/>    cpu_pct        = optional(number, 90)<br/>    cpu_sustain    = optional(string, "5m")<br/>    transfer_bytes = optional(string, "50GiB")<br/>  })</pre> | `{}` | no |
| <a name="input_dashboard_discord_webhook_url_param"></a> [dashboard\_discord\_webhook\_url\_param](#input\_dashboard\_discord\_webhook\_url\_param) | SSM parameter NAME holding the Discord incoming-webhook URL. Created out-of-band (like wg\_server\_private\_key\_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_DISCORD\_WEBHOOK\_URL. The value is a secret and is never output. Empty string (default) disables the Discord transport — no DASHBOARD\_DISCORD\_WEBHOOK\_URL line is written. | `string` | `""` | no |
| <a name="input_dashboard_release_tag"></a> [dashboard\_release\_tag](#input\_dashboard\_release\_tag) | Pinned GitHub Release tag (e.g. "v1.2.3") of the wireguard-dashboard binary to fetch at first boot. When non-empty, user-data downloads the asset from the public release over HTTPS and verifies it against the release's SHA256SUMS before installing it (no S3, no IAM data-plane grant). Empty string (default) disables dashboard provisioning. This is the single source of truth for the running version — bumping it re-renders user-data, rolls a new launch-template version, and replaces the instance. | `string` | `""` | no |
| <a name="input_dashboard_slack_bot_token_param"></a> [dashboard\_slack\_bot\_token\_param](#input\_dashboard\_slack\_bot\_token\_param) | SSM parameter NAME holding the Slack bot token used with chat.postMessage. Created out-of-band (like wg\_server\_private\_key\_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_SLACK\_BOT\_TOKEN. The value is a secret and is never output. Empty string (default) disables the Slack-bot transport — no DASHBOARD\_SLACK\_BOT\_TOKEN line is written. Pair with dashboard\_slack\_channel. | `string` | `""` | no |
| <a name="input_dashboard_slack_channel"></a> [dashboard\_slack\_channel](#input\_dashboard\_slack\_channel) | Non-secret Slack channel id or name (e.g. "C0123456789" or "#alerts") the Slack-bot transport posts to via chat.postMessage. Plain string seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_SLACK\_CHANNEL. Empty string (default) omits the line. Pair with dashboard\_slack\_bot\_token\_param. | `string` | `""` | no |
| <a name="input_dashboard_telegram_chat_id"></a> [dashboard\_telegram\_chat\_id](#input\_dashboard\_telegram\_chat\_id) | Non-secret Telegram chat id (e.g. "-1001234567890") the Telegram transport sends to. Plain string seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_TELEGRAM\_CHAT\_ID. Empty string (default) omits the line. Pair with dashboard\_telegram\_token\_param. | `string` | `""` | no |
| <a name="input_dashboard_telegram_token_param"></a> [dashboard\_telegram\_token\_param](#input\_dashboard\_telegram\_token\_param) | SSM parameter NAME holding the Telegram bot token. Created out-of-band (like wg\_server\_private\_key\_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_TELEGRAM\_TOKEN. The value is a secret and is never output. Empty string (default) disables the Telegram transport — no DASHBOARD\_TELEGRAM\_TOKEN line is written. Pair with dashboard\_telegram\_chat\_id. | `string` | `""` | no |
| <a name="input_dashboard_webhook_url_param"></a> [dashboard\_webhook\_url\_param](#input\_dashboard\_webhook\_url\_param) | SSM parameter NAME holding the Slack-compatible alert webhook URL. Created out-of-band (like wg\_server\_private\_key\_param), not by Terraform; read at apply with `with_decryption = true` and seeded into /etc/wireguard-dashboard/alerts.env as DASHBOARD\_WEBHOOK\_URL. The value is a secret and is never output. Empty string (default) disables the webhook seed — alerts.env is still written with the non-secret knobs, but no DASHBOARD\_WEBHOOK\_URL line, so the dashboard's alerting stays dormant until the operator creates the SSM param and sets this name. | `string` | `""` | no |
| <a name="input_env"></a> [env](#input\_env) | The name of environment for WireGuard. Used to differentiate multiple deployments. | `string` | n/a | yes |
| <a name="input_github_repo"></a> [github\_repo](#input\_github\_repo) | GitHub repository slug (owner/name) used BOTH for the raw scripts/install.sh fetch and the dashboard release download at first boot. For install.sh it is combined with install\_script\_ref into the raw-content URL https://raw.githubusercontent.com/<repo>/<ref>/scripts/install.sh; for the dashboard it is combined with dashboard\_release\_tag into the public asset base URL https://github.com/<repo>/releases/download/<tag>/. Anonymous fetch/download requires the repo to be public. | `string` | `"vkatrichenko/wireguard-vpn"` | no |
| <a name="input_install_script_ref"></a> [install\_script\_ref](#input\_install\_script\_ref) | A commit SHA or tag pinning the exact scripts/install.sh version fetched at boot from github\_repo. Must point at a ref on the PUBLIC default branch where the install.sh exists, otherwise the raw fetch 404s and boot aborts. Bumping this re-renders user-data, rolls a new launch-template version, and replaces the instance. | `string` | `"main"` | no |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | Optional explicit instance-type override. When null (default), the module uses the architecture's default (t3a.micro for x86\_64, t4g.micro for arm64). A non-null value overrides the architecture-derived default. | `string` | `null` | no |
| <a name="input_preconfigured_ssh_key_id"></a> [preconfigured\_ssh\_key\_id](#input\_preconfigured\_ssh\_key\_id) | A SSH public key ID to add to the VPN instance. | `string` | `null` | no |
| <a name="input_project_name"></a> [project\_name](#input\_project\_name) | Name of the project | `string` | n/a | yes |
| <a name="input_subnet_id"></a> [subnet\_id](#input\_subnet\_id) | A list of subnets. May be a single subnet, but it must be an element in a list. | `string` | n/a | yes |
| <a name="input_tags"></a> [tags](#input\_tags) | A map of tags to assign to resources. | `map(string)` | n/a | yes |
| <a name="input_use_eip"></a> [use\_eip](#input\_use\_eip) | Whether to enable Elastic IP switching code in user-data on wg server startup. If true, eip\_id must also be set to the ID of the Elastic IP. | `bool` | `true` | no |
| <a name="input_vpc_id"></a> [vpc\_id](#input\_vpc\_id) | The VPC ID in which Terraform will launch the resources. | `string` | n/a | yes |
| <a name="input_wg_persistent_keepalive"></a> [wg\_persistent\_keepalive](#input\_wg\_persistent\_keepalive) | Persistent Keepalive - useful for helping connection stability over NATs. | `number` | `25` | no |
| <a name="input_wg_server_net"></a> [wg\_server\_net](#input\_wg\_server\_net) | IP range for vpn server - make sure your Client ips are in this range but not the specific ip i.e. not .1 | `string` | n/a | yes |
| <a name="input_wg_server_port"></a> [wg\_server\_port](#input\_wg\_server\_port) | Port for the vpn server. | `number` | `51820` | no |
| <a name="input_wg_server_private_key_param"></a> [wg\_server\_private\_key\_param](#input\_wg\_server\_private\_key\_param) | The SSM parameter containing the WG server private key. | `string` | n/a | yes |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_instance_arn"></a> [instance\_arn](#output\_instance\_arn) | ARN of the WireGuard EC2 instance — used by the dashboard module to scope SSM document grants. |
| <a name="output_security_group_id"></a> [security\_group\_id](#output\_security\_group\_id) | n/a |
<!-- END_TF_DOCS -->
