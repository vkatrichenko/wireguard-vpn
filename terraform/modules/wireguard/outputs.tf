output "security_group_id" {
  value = aws_security_group.sg_wireguard_external.id
}

output "instance_arn" {
  description = "ARN of the WireGuard EC2 instance — used by the dashboard module to scope SSM document grants."
  value       = aws_instance.wireguard.arn
}
