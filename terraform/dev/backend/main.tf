resource "aws_s3_bucket" "terraform_state" {
  bucket              = local.state_bucket
  object_lock_enabled = true

  lifecycle {
    prevent_destroy = true
  }

  tags = {
    Name        = local.state_bucket
    Description = "S3 Remote Terraform State"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "s3_encryption" {
  bucket = aws_s3_bucket.terraform_state.id
  rule {
    apply_server_side_encryption_by_default {
      # SSE-KMS with the AWS-managed `aws/s3` key (spec 020 slice 5, C1): setting
      # sse_algorithm = "aws:kms" WITHOUT kms_master_key_id selects the managed
      # key — no custom CMK to own or pay for. This is an in-place bucket-config
      # update; existing state objects keep their old encryption until the next
      # state write re-encrypts them.
      sse_algorithm = "aws:kms"
    }
  }
}

resource "aws_s3_bucket_versioning" "s3_versioning" {
  bucket = aws_s3_bucket.terraform_state.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "terraform_state" {
  bucket = aws_s3_bucket.terraform_state.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}
