# dev

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | = 1.14.8 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | = 6.41.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | 6.41.0 |

## Modules

| Name | Source | Version |
|------|--------|---------|
| <a name="module_dashboard"></a> [dashboard](#module\_dashboard) | ../modules/dashboard | n/a |
| <a name="module_github_oidc"></a> [github\_oidc](#module\_github\_oidc) | ../modules/github-oidc | n/a |
| <a name="module_network"></a> [network](#module\_network) | ../modules/network/vpc | n/a |
| <a name="module_wireguard"></a> [wireguard](#module\_wireguard) | ../modules/wireguard | n/a |

## Resources

| Name | Type |
|------|------|
| [aws_ami.ubuntu_2404](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ami) | data source |

## Inputs

No inputs.

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_dashboard_ci_build_role_arn"></a> [dashboard\_ci\_build\_role\_arn](#output\_dashboard\_ci\_build\_role\_arn) | ARN to set as `role-to-assume` in the dashboard CI build workflow. |
| <a name="output_dashboard_ci_deploy_role_arn"></a> [dashboard\_ci\_deploy\_role\_arn](#output\_dashboard\_ci\_deploy\_role\_arn) | ARN to set as `role-to-assume` in the dashboard CI deploy workflow. |
| <a name="output_dashboard_deploy_ssm_document_name"></a> [dashboard\_deploy\_ssm\_document\_name](#output\_dashboard\_deploy\_ssm\_document\_name) | SSM document name the deploy workflow passes to `aws ssm send-command --document-name`. |
<!-- END_TF_DOCS -->
