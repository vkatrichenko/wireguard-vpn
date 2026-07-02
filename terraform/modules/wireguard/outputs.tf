output "security_group_id" {
  value = aws_security_group.sg_wireguard_external.id
}

output "instance_arn" {
  description = "ARN of the WireGuard EC2 instance — used by the dashboard module to scope SSM document grants."
  value       = aws_instance.wireguard.arn
}

# --- S3 client-list bridge (spec 018) ---------------------------------------
# Informational: the bucket name for the cloud-mode client store, so an operator
# can locate/inspect clients.json. CLOUD-ONLY: the bucket is count-gated (cloud
# mode), so this is an EMPTY string in local mode (no bucket); the [0] index is
# guarded and only reached when the resource exists. The drift check and its data
# source live INSIDE the module (checks.tf) and reference the S3 resources +
# local.clients_canonical_json directly, so no key/canonical outputs are needed.
output "client_list_bucket" {
  description = "Name of the S3 bucket holding the canonical client list (clients.json) in cloud mode. Empty string in local mode (no bucket)."
  value       = local.client_store_enabled ? aws_s3_bucket.client_list[0].bucket : ""
}
