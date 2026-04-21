resource "aws_eip" "wireguard" {
  count = var.use_eip ? 1 : 0

  domain = "vpc"
  tags = {
    Name = "wireguard-${var.env}"
  }
}

resource "aws_s3_bucket" "health_check" {
  bucket        = "${var.project_name}-${var.env}-wireguard-ec2-health-check"
  force_destroy = true # Allows terraform to delete the bucket even if it has signal files

  tags = var.tags
}

resource "aws_launch_template" "wireguard" {
  name = "wireguard-${var.env}"

  image_id = var.ami_id
  key_name = var.preconfigured_ssh_key_id != null ? var.preconfigured_ssh_key_id : aws_key_pair.ssh[0].id

  iam_instance_profile {
    name = aws_iam_instance_profile.wireguard_profile[0].name
  }

  network_interfaces {
    # This gives the NEW instance a temporary public IP at birth.
    # It can now run 'apt-get' and talk to S3 using the Internet Gateway.
    associate_public_ip_address = true

    # You MUST move the security groups into the network_interface block 
    # if you use the network_interface block at all.
    security_groups = local.security_groups_ids
    subnet_id       = var.subnet_id

    delete_on_termination = true
  }

  user_data = base64encode(local.user_data)
}

resource "aws_instance" "wireguard" {
  instance_type = var.instance_type
  # vpc_security_group_ids = local.security_groups_ids
  # subnet_id              = var.subnet_id

  launch_template {
    id      = aws_launch_template.wireguard.id
    version = aws_launch_template.wireguard.latest_version
  }

  lifecycle {
    create_before_destroy = true
    ignore_changes        = [user_data, user_data_base64]
  }

  tags = {
    "Name" = "wireguard-${var.env}"
  }
}

# This resource forces Terraform to wait until the script signals S3
resource "null_resource" "status_check" {
  triggers = {
    instance_id = aws_instance.wireguard.id
  }

  provisioner "local-exec" {
    command = <<EOT
      for i in {1..30}; do
        if aws s3 ls s3://${aws_s3_bucket.health_check.bucket}/${aws_instance.wireguard.id}.ready; then
          exit 0
        fi
        echo "Waiting for WireGuard to start..."
        sleep 10
      done
      echo "Health check failed!"
      exit 1
    EOT
  }
}

resource "aws_eip_association" "wireguard" {
  count = var.use_eip ? 1 : 0

  depends_on = [
    aws_instance.wireguard,
    aws_eip.wireguard,
    null_resource.status_check
  ]

  lifecycle {
    create_before_destroy = true
  }

  instance_id   = aws_instance.wireguard.id
  allocation_id = aws_eip.wireguard[0].id
}
