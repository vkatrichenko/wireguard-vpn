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

# Health-check bucket posture (spec 020 slice 5, C5) — mirrors the client_list
# bucket in client_store.tf but UNCONDITIONAL: the health-check bucket always
# exists (no cloud/local gating), so these carry no count/[0].
resource "aws_s3_bucket_public_access_block" "health_check" {
  bucket = aws_s3_bucket.health_check.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "health_check" {
  bucket = aws_s3_bucket.health_check.id

  rule {
    apply_server_side_encryption_by_default {
      # SSE-S3 (AES256): the bucket only holds empty <instance-id>.ready signal
      # files — nothing sensitive — so S3-managed encryption is sufficient and
      # avoids a KMS key + grants, matching the client_list posture.
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_versioning" "health_check" {
  bucket = aws_s3_bucket.health_check.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_launch_template" "wireguard" {
  name = "wireguard-${var.env}"

  image_id = local.effective_ami_id

  iam_instance_profile {
    name = aws_iam_instance_profile.wireguard_profile.name
  }

  # Require IMDSv2 (spec 020 slice 5, C2). The user-data wrapper already uses the
  # IMDSv2 token flow, so enforcing http_tokens = "required" won't break boot.
  metadata_options {
    http_endpoint               = "enabled"
    http_tokens                 = "required"
    http_put_response_hop_limit = 1
  }

  # Encrypt the root EBS volume (spec 020 slice 5, C4). device_name must match the
  # AMI's root device — see local.root_device_name for how it's resolved.
  block_device_mappings {
    device_name = local.root_device_name

    ebs {
      encrypted = true
    }
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
  instance_type = local.effective_instance_type
  # vpc_security_group_ids = local.security_groups_ids
  # subnet_id              = var.subnet_id

  launch_template {
    id      = aws_launch_template.wireguard.id
    version = aws_launch_template.wireguard.latest_version
  }

  lifecycle {
    create_before_destroy = true
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
