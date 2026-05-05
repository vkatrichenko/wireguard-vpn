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
| <a name="module_network"></a> [network](#module\_network) | ../modules/network/vpc | n/a |
| <a name="module_wireguard"></a> [wireguard](#module\_wireguard) | ../modules/wireguard | n/a |

## Resources

| Name | Type |
|------|------|
| [aws_ami.ubuntu_2404](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/ami) | data source |

## Inputs

No inputs.

## Outputs

No outputs.
<!-- END_TF_DOCS -->
