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

  # Dashboard artifact read access. Only emitted when the caller wires in
  # `dashboard_artifact_bucket_arn`; null keeps the instance policy unchanged.
  dynamic "statement" {
    for_each = var.dashboard_artifact_bucket_arn != null ? [1] : []

    content {
      sid     = "S3GetObjectDashboardBinary"
      actions = ["s3:GetObject"]
      resources = [
        "${var.dashboard_artifact_bucket_arn}/latest/*",
        "${var.dashboard_artifact_bucket_arn}/main-*/*",
      ]
    }
  }

  dynamic "statement" {
    for_each = var.dashboard_artifact_bucket_arn != null ? [1] : []

    content {
      sid       = "S3ListBucketDashboardArtifacts"
      actions   = ["s3:ListBucket"]
      resources = [var.dashboard_artifact_bucket_arn]

      condition {
        test     = "StringLike"
        variable = "s3:prefix"
        values   = ["latest/*", "main-*/*"]
      }
    }
  }
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

# SSM-managed-node membership for the dashboard CI deploy path.
# Without this attachment the instance's SSM agent cannot call
# UpdateInstanceInformation, so SendCommand returns
# "InvalidInstanceId: Instances not in a valid state for account".
# AmazonSSMManagedInstanceCore is the AWS-recommended baseline and covers
# ssmmessages/ec2messages plus the minimal S3/KMS perms the agent needs
# to fetch SSM documents.
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
