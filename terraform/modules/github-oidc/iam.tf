# GitHub Actions OIDC-assumable IAM roles.
#
# The module accepts a map of role definitions via var.roles and provisions one
# IAM role per entry. Each role is assumable only by the GitHub Actions OIDC
# provider and only for the exact :sub claim configured in the entry.
#
# Permissions for each role are assembled from two complementary inputs:
#
#   1. s3_put_object — a typed shorthand for the common "CI uploads artifacts
#      to S3" case. Each entry yields a single statement granting s3:PutObject
#      against the listed prefixes under a bucket ARN.
#   2. inline_policy_json — an escape hatch for arbitrary statements. Merged
#      via aws_iam_policy_document.source_policy_documents, which concatenates
#      statements (sids must be unique across the merged set).
#
# StringEquals (not StringLike) is used on the :sub condition so feature
# branches and tags cannot assume a role unless explicitly enumerated by the
# consumer.

data "aws_iam_policy_document" "assume" {
  for_each = var.roles

  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.arn]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }

    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:sub"
      values   = [each.value.subject]
    }
  }
}

data "aws_iam_policy_document" "role_policy" {
  for_each = var.roles

  # Typed s3_put_object statements — one statement per (bucket, prefixes) entry.
  dynamic "statement" {
    for_each = each.value.s3_put_object
    content {
      sid     = "S3PutObject${statement.key}"
      actions = ["s3:PutObject"]
      resources = [
        for p in statement.value.prefixes :
        "${statement.value.bucket_arn}/${p}"
      ]
    }
  }

  # Escape hatch — merge raw JSON via source_policy_documents. The module
  # always emits the doc from the dynamic blocks above; if inline JSON was
  # supplied, source_policy_documents merges its statements in.
  source_policy_documents = (
    each.value.inline_policy_json != ""
    ? [each.value.inline_policy_json]
    : []
  )

  lifecycle {
    precondition {
      condition     = length(each.value.s3_put_object) > 0 || each.value.inline_policy_json != ""
      error_message = "github-oidc role '${each.key}' has no permissions: provide s3_put_object entries or inline_policy_json (or both)."
    }
  }
}

resource "aws_iam_role" "github_role" {
  for_each = var.roles

  name               = "tf-github-actions-${each.value.name_suffix}"
  assume_role_policy = data.aws_iam_policy_document.assume[each.key].json

  tags = var.tags
}

resource "aws_iam_role_policy" "github_role" {
  for_each = var.roles

  name   = "tf-github-actions-${each.value.name_suffix}"
  role   = aws_iam_role.github_role[each.key].id
  policy = data.aws_iam_policy_document.role_policy[each.key].json
}
