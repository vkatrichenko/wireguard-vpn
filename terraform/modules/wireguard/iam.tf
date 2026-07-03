data "aws_iam_policy_document" "ec2_assume_role" {
  statement {
    actions = [
      "sts:AssumeRole",
    ]

    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "wireguard_policy_doc" {
  statement {
    actions = [
      "ec2:AssociateAddress",
    ]

    resources = ["*"]
  }
  # New S3 permission for signaling
  statement {
    actions   = ["s3:PutObject"]
    resources = ["${aws_s3_bucket.health_check.arn}/*"]
  }

  # S3 client-list backup (spec 019). CLOUD-ONLY: the statements are emitted only
  # when the store is enabled (cloud mode) — in local mode there is no bucket and
  # no grant. Least-privilege: read + write the SINGLE clients.json object. The
  # [0] index is safe here because the dynamic blocks iterate only when the
  # bucket exists.
  dynamic "statement" {
    for_each = local.client_store_enabled ? [1] : []

    content {
      actions = [
        "s3:GetObject",
        "s3:PutObject",
      ]
      resources = ["${aws_s3_bucket.client_list[0].arn}/clients.json"]
    }
  }

  # s3:ListBucket is REQUIRED, not optional, even though the dashboard only ever
  # touches one key. Since spec 019 the dashboard (not Terraform) creates
  # clients.json — so on a fresh box the very first `aws s3api get-object` hits a
  # key that does NOT exist yet. S3 only returns 404 NoSuchKey for a missing
  # object when the caller holds s3:ListBucket; WITHOUT it, S3 returns 403
  # AccessDenied instead (it refuses to confirm or deny the key's existence).
  # The dashboard's clientstore classifies a 403 as a hard error (not "absent"),
  # latches storeReady=false, and then skips every write-through — so clients.json
  # is never created and the backup silently never works. Granting ListBucket on
  # the bucket makes the missing-object read return a proper 404, which the
  # cold-seed path handles by writing the object. Scoped to this single dedicated
  # bucket (it holds only clients.json), so listing leaks nothing meaningful.
  dynamic "statement" {
    for_each = local.client_store_enabled ? [1] : []

    content {
      actions   = ["s3:ListBucket"]
      resources = [aws_s3_bucket.client_list[0].arn]
    }
  }

  # No dashboard-artifact S3 read statements: the dashboard binary is fetched at
  # boot from a public GitHub Release over HTTPS (spec 005), so the instance role
  # no longer needs s3:GetObject/s3:ListBucket on a private artifact bucket.
}

resource "aws_iam_policy" "wireguard_policy" {
  name        = "tf-wireguard-${var.env}"
  description = "Terraform Managed. Allows Wireguard instance to attach EIP."
  policy      = data.aws_iam_policy_document.wireguard_policy_doc.json
  count       = (var.use_eip ? 1 : 0) # only used for EIP mode
}

resource "aws_iam_role" "wireguard_role" {
  name               = "tf-wireguard-${var.env}"
  description        = "Terraform Managed. Role to allow Wireguard instance to attach EIP."
  path               = "/"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume_role.json
  count              = (var.use_eip ? 1 : 0) # only used for EIP mode
}

resource "aws_iam_role_policy_attachment" "wireguard_roleattach" {
  role       = aws_iam_role.wireguard_role[0].name
  policy_arn = aws_iam_policy.wireguard_policy[0].arn
  count      = (var.use_eip ? 1 : 0) # only used for EIP mode
}

# SSM-managed-node membership. Originally added for the (now-removed, spec 005)
# CI deploy path; retained for break-glass operator access — Session Manager
# (`aws ssm start-session`) gives a shell without opening SSH or relying on the
# WireGuard tunnel being up. AmazonSSMManagedInstanceCore is the AWS-recommended
# baseline (ssmmessages/ec2messages + the minimal perms the agent needs to
# register via UpdateInstanceInformation).
resource "aws_iam_role_policy_attachment" "wireguard_ssm_core" {
  role       = aws_iam_role.wireguard_role[0].name
  policy_arn = "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore"
  count      = (var.use_eip ? 1 : 0) # only used for EIP mode
}

resource "aws_iam_instance_profile" "wireguard_profile" {
  name  = "tf-wireguard-${var.env}"
  role  = aws_iam_role.wireguard_role[0].name
  count = (var.use_eip ? 1 : 0) # only used for EIP mode
}
