output "provider_arn" {
  description = "ARN of the GitHub Actions IAM OIDC provider (whether newly created by this module or read from the account via data source)."
  value       = local.arn
}

output "provider_url" {
  description = "Issuer URL of the GitHub Actions OIDC provider."
  value       = local.url
}

output "role_arns" {
  description = "Map of role-key → ARN for the IAM roles created from var.roles."
  value       = { for k, r in aws_iam_role.github_role : k => r.arn }
}
