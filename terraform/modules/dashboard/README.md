# dashboard

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
| [aws_s3_bucket.this](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket) | resource |
| [aws_s3_bucket_lifecycle_configuration.this](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket_lifecycle_configuration) | resource |
| [aws_s3_bucket_public_access_block.this](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket_public_access_block) | resource |
| [aws_s3_bucket_server_side_encryption_configuration.this](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket_server_side_encryption_configuration) | resource |
| [aws_s3_bucket_versioning.this](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/s3_bucket_versioning) | resource |
| [aws_ssm_document.deploy](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/resources/ssm_document) | resource |
| [aws_iam_policy_document.deploy](https://registry.terraform.io/providers/hashicorp/aws/6.41.0/docs/data-sources/iam_policy_document) | data source |

## Inputs

| Name | Description | Type | Default | Required |
|------|-------------|------|---------|:--------:|
| <a name="input_env"></a> [env](#input\_env) | The name of environment for the dashboard. Used to differentiate multiple deployments. | `string` | `""` | no |
| <a name="input_project_name"></a> [project\_name](#input\_project\_name) | Name of the project. Used to derive resource names. | `string` | n/a | yes |
| <a name="input_tags"></a> [tags](#input\_tags) | A map of tags to assign to resources. | `map(string)` | n/a | yes |
| <a name="input_target_instance_arn"></a> [target\_instance\_arn](#input\_target\_instance\_arn) | ARN of the EC2 instance the deploy SSM document targets (the WireGuard host running the dashboard binary). When null, the deploy SSM document and its associated policy output are not produced. | `string` | `null` | no |

## Outputs

| Name | Description |
|------|-------------|
| <a name="output_bucket_arn"></a> [bucket\_arn](#output\_bucket\_arn) | ARN of the dashboard artifact S3 bucket. |
| <a name="output_bucket_id"></a> [bucket\_id](#output\_bucket\_id) | ID of the dashboard artifact S3 bucket. |
| <a name="output_bucket_name"></a> [bucket\_name](#output\_bucket\_name) | Name of the dashboard artifact S3 bucket. |
| <a name="output_deploy_policy_json"></a> [deploy\_policy\_json](#output\_deploy\_policy\_json) | Inline IAM policy JSON granting ssm:SendCommand on the deploy document + target instance, plus ssm:GetCommandInvocation account-wide for status polling. Pass this to a github-oidc role's inline\_policy\_json so the GH Actions deploy can run the document. Empty when var.target\_instance\_arn is null. |
| <a name="output_deploy_ssm_document_name"></a> [deploy\_ssm\_document\_name](#output\_deploy\_ssm\_document\_name) | Name of the SSM document the CI deploy workflow invokes via aws ssm send-command. Empty when var.target\_instance\_arn is null. |
<!-- END_TF_DOCS -->
