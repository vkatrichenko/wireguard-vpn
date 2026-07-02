output "security_group_id" {
  value = aws_security_group.sg_wireguard_external.id
}

output "instance_arn" {
  description = "ARN of the WireGuard EC2 instance — used by the dashboard module to scope SSM document grants."
  value       = aws_instance.wireguard.arn
}

# --- S3 client-list backup (spec 019) ---------------------------------------
# Informational: the bucket name for the cloud-mode client-list backup, so an
# operator can locate/inspect clients.json. CLOUD-ONLY: the bucket is count-gated
# (cloud mode), so this is an EMPTY string in local mode (no bucket); the [0]
# index is guarded and only reached when the resource exists. Terraform does not
# read or manage the object (the dashboard owns it), so no key output is needed.
output "client_list_bucket" {
  description = "Name of the S3 bucket holding the dashboard-owned client-list backup (clients.json) in cloud mode. Empty string in local mode (no bucket)."
  value       = local.client_store_enabled ? aws_s3_bucket.client_list[0].bucket : ""
}
