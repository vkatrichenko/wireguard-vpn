# --- S3 client-list backup (spec 019 §2.1) ----------------------------------
# A dedicated, versioned S3 bucket holds the WireGuard client list (clients.json)
# as a pure durable BACKUP owned entirely by the dashboard: it restores from the
# object at boot and rewrites it on every UI edit. Terraform NEVER reads or seeds
# it — there is no drift detection and no Terraform-managed object; the dashboard
# 404-initializes clients.json on first boot. Kept SEPARATE from the health-check
# bucket (main.tf) — different lifecycle and access pattern.
#
# CLOUD-ONLY: every resource here is count-gated on local.client_store_enabled
# (== cloud mode). A default `local` apply provisions ZERO S3 — no bucket, no
# IAM grant — matching the functional spec's "local = no AWS store required". The
# [0] index is always safe because the reference sites below are themselves
# cloud-gated (locals/outputs) or live in a cloud-gated policy statement.
resource "aws_s3_bucket" "client_list" {
  count = local.client_store_enabled ? 1 : 0

  bucket = "${var.project_name}-${var.env}-wireguard-client-list"

  # force_destroy = true is the operator's explicit choice: it lets
  # `terraform destroy` delete this bucket even when it holds objects, AND wipes
  # the object's entire VERSION HISTORY along with it. Acceptable for a small VPN
  # whose client list is reconstructable from the dashboard's SQLite store; do not
  # enable this lightly on buckets whose version history is the only copy of the
  # data.
  force_destroy = true

  tags = merge(var.tags, {
    Name = "${var.project_name}-${var.env}-wireguard-client-list"
  })
}

resource "aws_s3_bucket_versioning" "client_list" {
  count = local.client_store_enabled ? 1 : 0

  bucket = aws_s3_bucket.client_list[0].id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "client_list" {
  count = local.client_store_enabled ? 1 : 0

  bucket = aws_s3_bucket.client_list[0].id

  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "client_list" {
  count = local.client_store_enabled ? 1 : 0

  bucket = aws_s3_bucket.client_list[0].id

  rule {
    apply_server_side_encryption_by_default {
      # SSE-S3 (AES256), not KMS: the client list is names + tunnel IPs + PUBLIC
      # keys — not sensitive — so plain S3-managed encryption is sufficient and
      # avoids a KMS key + its grants.
      sse_algorithm = "AES256"
    }
  }
}
