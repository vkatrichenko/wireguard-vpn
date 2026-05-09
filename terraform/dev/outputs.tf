output "dashboard_ci_build_role_arn" {
  description = "ARN to set as `role-to-assume` in the dashboard CI build workflow."
  value       = module.github_oidc.role_arns["dashboard-ci-build"]
}
