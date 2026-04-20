## SSH KEY FOR EC2 INSTANCES ##
# Generate SSH key (RSA for EC2)
resource "tls_private_key" "ssh" {
  count     = var.preconfigured_ssh_key_id != null ? 0 : 1
  algorithm = "RSA"
  rsa_bits  = 4096
}

# Register the public key with EC2
resource "aws_key_pair" "ssh" {
  count      = var.preconfigured_ssh_key_id != null ? 0 : 1
  key_name   = "${var.project_name}-${var.env}-wireguard-ec2"
  public_key = tls_private_key.ssh[0].public_key_openssh
  tags       = var.tags
}

# Store the private key in SSM Parameter Store (encrypted)
resource "aws_ssm_parameter" "ssh_private_key" {
  count       = var.preconfigured_ssh_key_id != null ? 0 : 1
  name        = "/config/wireguard/ssh/private-key"
  description = "SSH private key for EC2 access"
  type        = "SecureString"
  value       = tls_private_key.ssh[0].private_key_pem
  tags        = var.tags
  # optional: key_id = "alias/aws/ssm" or a custom KMS key
}
