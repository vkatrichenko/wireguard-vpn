# --- S3 client-list bridge (spec 018 §2.2) ----------------------------------
# A dedicated, versioned S3 object holds the WireGuard client list as canonical
# JSON (locals.clients_canonical_json). It is the durable bridge between the
# dashboard (reads at boot, writes on every UI edit) and Terraform (seeds it
# once, warns on drift via the root-module `check`). Kept SEPARATE from the
# health-check bucket (main.tf) — different lifecycle and access pattern.
#
# CLOUD-ONLY: every resource here is count-gated on local.client_store_enabled
# (== cloud mode). A default `local` apply provisions ZERO S3 — no bucket, no
# object, no IAM grant, no drift-check read — matching the functional spec's
# "local = no AWS store required". The [0] index is always safe because the
# reference sites below are themselves cloud-gated (locals/outputs) or live in a
# cloud-gated policy statement.
resource "aws_s3_bucket" "client_list" {
  count = local.client_store_enabled ? 1 : 0

  bucket = "${var.project_name}-${var.env}-wireguard-client-list"

  # force_destroy = true is the operator's explicit choice: it lets
  # `terraform destroy` delete this bucket even when it holds objects, AND wipes
  # the object's entire VERSION HISTORY along with it. Acceptable for a small VPN
  # whose client list is reconstructable from clients_config; do not enable this
  # lightly on buckets whose version history is the only copy of the data.
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

# Seed object — Terraform writes the canonical client list ONCE. ignore_changes
# on [content, etag] makes this UI-authoritative: after the first apply Terraform
# never overwrites the dashboard's live writes; divergence is surfaced (not
# reverted) by the root-module drift `check`. Deleting the object is the escape
# hatch to force a re-seed on the next apply.
resource "aws_s3_object" "clients" {
  count = local.client_store_enabled ? 1 : 0

  bucket       = aws_s3_bucket.client_list[0].id
  key          = "clients.json"
  content      = local.clients_canonical_json
  content_type = "application/json"

  lifecycle {
    ignore_changes = [content, etag]
  }
}
