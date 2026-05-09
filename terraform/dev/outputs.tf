output "dashboard_ci_build_role_arn" {
  description = "ARN to set as `role-to-assume` in the dashboard CI build workflow."
  value       = module.github_oidc.role_arns["dashboard-ci-build"]
}

output "dashboard_ci_deploy_role_arn" {
  description = "ARN to set as `role-to-assume` in the dashboard CI deploy workflow."
  value       = module.github_oidc.role_arns["dashboard-ci-deploy"]
}

output "dashboard_deploy_ssm_document_name" {
  description = "SSM document name the deploy workflow passes to `aws ssm send-command --document-name`."
  value       = module.dashboard.deploy_ssm_document_name
}
