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
| [aws_instance.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/instance) | resource |
| [aws_key_pair.ssh](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/key_pair) | resource |
| [aws_launch_template.wireguard](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/launch_template) | resource |
| [aws_s3_bucket.health_check](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket) | resource |
| [aws_security_group.sg_wireguard_external](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/security_group) | resource |
| [aws_ssm_parameter.ssh_private_key](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/ssm_parameter) | resource |
| [null_resource.status_check](https://registry.terraform.io/providers/hashicorp/null/3.2.4/docs/resources/resource) | resource |
| [tls_private_key.ssh](https://registry.terraform.io/providers/hashicorp/tls/4.0.3/docs/resources/private_key) | resource |
| [aws_iam_policy_document.ec2_assume_role](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.wireguard_policy_doc](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |
| [aws_ssm_parameter.wg_server_private_key](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ssm_parameter) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_additional_security_group_ids"></a> [additional\_security\_group\_ids](#input\_additional\_security\_group\_ids) | Additional security groups if provided, default empty. | `list(string)` | <pre>[<br/>  ""<br/>]</pre> | no |
| <a name="input_ami_id"></a> [ami\_id](#input\_ami\_id) | The AWS AMI to use for the WG server, defaults to the latest Ubuntu 16.04 AMI if not specified. | `string` | `null` | no |
| <a name="input_clients_config"></a> [clients\_config](#input\_clients\_config) | List of maps of client IPs and public keys. See Usage in README for details. | `any` | n/a | yes |
| <a name="input_dashboard_artifact_bucket_arn"></a> [dashboard\_artifact\_bucket\_arn](#input\_dashboard\_artifact\_bucket\_arn) | ARN of the S3 bucket that holds the web-dashboard binary artifacts. When non-null, the EC2 instance role gains scoped read access to objects under `latest/*` and `main-*/*`. When null (default), no dashboard read statements are added to the instance policy. Wired in by `dev/main.tf` once the dashboard module is composed in. | `string` | `null` | no |
| <a name="input_dashboard_artifact_bucket_name"></a> [dashboard\_artifact\_bucket\_name](#input\_dashboard\_artifact\_bucket\_name) | Name of the S3 bucket that hosts the web-dashboard binary artifacts. When non-null, first-boot user-data downloads `s3://<bucket>/latest/wireguard-dashboard`, installs it under `/opt/wireguard-dashboard/bin/`, and starts a `wireguard-dashboard.service` systemd unit bound to the WireGuard tunnel IP. When null (default), the dashboard is not provisioned and user-data behaves as before. Paired with `dashboard_artifact_bucket_arn` (which gates the IAM read permissions); both are wired in by `dev/main.tf` once the dashboard module is composed in. | `string` | `null` | no |
| <a name="input_env"></a> [env](#input\_env) | The name of environment for WireGuard. Used to differentiate multiple deployments. | `string` | n/a | yes |
| <a name="input_instance_type"></a> [instance\_type](#input\_instance\_type) | The machine type to launch, some machines may offer higher throughput for higher use cases. | `string` | `"t3a.micro"` | no |
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
| <a name="output_security_group_id"></a> [security\_group\_id](#output\_security\_group\_id) | n/a |
<!-- END_TF_DOCS -->
