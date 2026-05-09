output "bucket_name" {
  description = "Name of the dashboard artifact S3 bucket."
  value       = aws_s3_bucket.this.bucket
}

output "bucket_arn" {
  description = "ARN of the dashboard artifact S3 bucket."
  value       = aws_s3_bucket.this.arn
}

output "bucket_id" {
  description = "ID of the dashboard artifact S3 bucket."
  value       = aws_s3_bucket.this.id
}

output "deploy_ssm_document_name" {
  description = "Name of the SSM document the CI deploy workflow invokes via aws ssm send-command. Empty when var.target_instance_arn is null."
  value       = try(aws_ssm_document.deploy[0].name, "")
}

output "deploy_policy_json" {
  description = "Inline IAM policy JSON granting ssm:SendCommand on the deploy document + target instance, plus ssm:GetCommandInvocation account-wide for status polling. Pass this to a github-oidc role's inline_policy_json so the GH Actions deploy can run the document. Empty when var.target_instance_arn is null."
  value       = try(data.aws_iam_policy_document.deploy[0].json, "")
}
