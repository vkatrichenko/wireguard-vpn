# --- Client-list drift detection (spec 018 §2.3) ----------------------------
# Warn-only, CLOUD-ONLY. Compares the live S3 client list against the canonical
# list Terraform seeded from clients_config. The dashboard is authoritative — on
# every UI edit it rewrites the S3 object (cloud mode), so this check NEVER
# reverts anything; it only surfaces a WARNING on plan/apply when the live list
# has diverged from clients_config, so the operator can reconcile the git record
# (or accept the UI change). Lives in the module because the whole S3 client
# store lives here (`check` blocks are allowed in child modules on TF >= 1.5).
#
# In LOCAL mode this is a pure no-op: the data source below is count-gated to
# zero (no S3 read, no warning, no error), and the check's assert short-circuits
# to `true` before ever touching the (empty) data source. A default `local` plan
# is therefore completely silent.
#
# Why a TOP-LEVEL count-gated data source (not a scoped data source inside the
# check): a scoped data source cannot be count-gated, so in local mode it would
# read a non-existent bucket and emit a perpetual warning on every plan — exactly
# the noise we are eliminating. Count-gating requires a top-level data source.
#
# Cold-start / first-apply safety: a top-level data source that errors is
# normally a BLOCKING plan error (unlike a scoped-in-check one, which only
# warns). `depends_on = [aws_s3_object.clients]` makes Terraform DEFER the read
# to the apply phase whenever the seed object has pending changes — including the
# very first apply, when the object does not exist yet. By the time the read runs
# in apply, the seed object has been created, so the read succeeds instead of
# failing with NoSuchKey. In steady state (no changes to the seed object) the
# read happens at plan time, so drift is visible in `terraform plan`. Deleting
# the object out-of-band (the re-seed escape hatch) makes the seed object plan to
# recreate, which again defers the read to apply — no spurious error.
#
# Comparison strategy — DECODED STRUCTURES (`body`), not raw bytes: the AWS
# provider populates `data.aws_s3_object.body` for human-readable content types
# ("text/*" or "application/json"; the seed object is content_type
# "application/json", so body is populated). We jsondecode BOTH sides and compare
# the resulting tuples/objects, so key ordering or whitespace differences between
# Terraform's jsonencode and the dashboard's Go serializer can never register as
# phantom drift — only a genuine difference in the {name,address,public_key} set
# (or ordering by address) does. No hash fallback is needed because
# application/json reliably yields `body`.
data "aws_s3_object" "client_list_live" {
  count = local.client_store_enabled ? 1 : 0

  bucket = aws_s3_bucket.client_list[0].bucket
  key    = aws_s3_object.clients[0].key

  # Defer the read to apply on the first run / re-seed so a not-yet-existing
  # object never hard-fails the plan (see the header note above).
  depends_on = [aws_s3_object.clients]
}

check "client_list_drift" {
  assert {
    # Short-circuits to `true` in local mode (left operand), so the count-gated
    # data source is never indexed there. In cloud mode it requires the live
    # decoded list to equal the canonical decoded list.
    condition = !local.client_store_enabled || (
      length(data.aws_s3_object.client_list_live) > 0 &&
      jsondecode(data.aws_s3_object.client_list_live[0].body) == jsondecode(local.clients_canonical_json)
    )
    error_message = format(
      "WireGuard client list drift: the live S3 object s3://%s/%s differs from clients_config. The dashboard (UI) is authoritative and has NOT been reverted; reconcile by updating clients_config to match the live list, or change the list back in the UI. To force a re-seed from clients_config, delete the S3 object and re-apply.",
      one(aws_s3_bucket.client_list[*].bucket),
      one(aws_s3_object.clients[*].key),
    )
  }
}
