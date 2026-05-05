resource "aws_s3_bucket" "this" {
  bucket        = local.artifact_bucket_name
  force_destroy = false # Prevent accidental drops while CI-built dashboard images are present.

  tags = merge(var.tags, {
    Name = local.artifact_bucket_name
  })
}

resource "aws_s3_bucket_versioning" "this" {
  bucket = aws_s3_bucket.this.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  bucket = aws_s3_bucket.this.id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  bucket = aws_s3_bucket.this.id

  # Keep the 30 newest noncurrent versions under latest/, expire older noncurrents
  # after they have been noncurrent for at least 30 days.
  rule {
    id     = "expire-noncurrent-latest"
    status = "Enabled"

    filter {
      prefix = "latest/"
    }

    noncurrent_version_expiration {
      noncurrent_days           = 30
      newer_noncurrent_versions = 30
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  # Expire current versions of per-commit artifacts (main-<sha>/...) after 60 days.
  rule {
    id     = "expire-main-sha"
    status = "Enabled"

    filter {
      prefix = "main-"
    }

    expiration {
      days = 60
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}
