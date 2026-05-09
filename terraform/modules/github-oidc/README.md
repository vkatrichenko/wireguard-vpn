# github-oidc

<!-- BEGIN_TF_DOCS -->
## Requirements

| Name | Version |
|------|---------|
| <a name="requirement_terraform"></a> [terraform](#requirement\_terraform) | = 1.14.8 |
| <a name="requirement_aws"></a> [aws](#requirement\_aws) | = 6.41.0 |

## Providers

| Name | Version |
|------|---------|
| <a name="provider_aws"></a> [aws](#provider\_aws) | = 6.41.0 |

## Modules

No modules.

## Resources

| Name | Type |
|------|------|
| [aws_iam_openid_connect_provider.github](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_openid_connect_provider) | resource |
| [aws_iam_role.github_role](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_role) | resource |
| [aws_iam_role_policy.github_role](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/iam_role_policy) | resource |
| [aws_iam_openid_connect_provider.github](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_openid_connect_provider) | data source |
| [aws_iam_policy_document.assume](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |
| [aws_iam_policy_document.role_policy](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_roles"></a> [roles](#input\_roles) | Map of IAM role definitions keyed by role short-name. Each role gets a trust policy assumable via the GitHub OIDC provider, restricted to the supplied subject claim. Permissions are assembled from `s3_put_object` (typed shorthand for the common 'CI uploads to S3' case) and `inline_policy_json` (escape hatch for arbitrary statements). Both are optional; both apply when set; neither set yields a role with trust-only and no permissions. | <pre>map(object({<br/>    name_suffix = string<br/>    subject     = string<br/><br/>    # Typed shorthand: a list of (bucket_arn, prefixes) entries. The module<br/>    # builds a single policy statement per entry: actions=s3:PutObject,<br/>    # resources = ["${bucket_arn}/${prefix}", ...].<br/>    s3_put_object = optional(list(object({<br/>      bucket_arn = string<br/>      prefixes   = list(string)<br/>    })), [])<br/><br/>    # Escape hatch: a raw IAM policy JSON string (typically the output of<br/>    # data.aws_iam_policy_document.<name>.json) appended as additional<br/>    # statements. Use when s3_put_object isn't expressive enough.<br/>    inline_policy_json = optional(string, "")<br/>  }))</pre> | `{}` | no |
| <a name="input_tags"></a> [tags](#input\_tags) | A map of tags to assign to resources. | `map(string)` | n/a | yes |
| <a name="input_use_existing"></a> [use\_existing](#input\_use\_existing) | When true, the module reads the existing GitHub Actions OIDC provider from the account via a data source instead of creating a new one. Use this when the AWS account already has a `token.actions.githubusercontent.com` OIDC provider registered (e.g. by a sibling repo or a previous Terraform run). When false (default), the module creates the provider. | `bool` | `false` | no |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_provider_arn"></a> [provider\_arn](#output\_provider\_arn) | ARN of the GitHub Actions IAM OIDC provider (whether newly created by this module or read from the account via data source). |
| <a name="output_provider_url"></a> [provider\_url](#output\_provider\_url) | Issuer URL of the GitHub Actions OIDC provider. |
| <a name="output_role_arns"></a> [role\_arns](#output\_role\_arns) | Map of role-key → ARN for the IAM roles created from var.roles. |
<!-- END_TF_DOCS -->
