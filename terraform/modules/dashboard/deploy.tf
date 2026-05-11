# SSM document + IAM policy document for the dashboard CI deploy pipeline.
#
# The CI workflow (assuming the github-oidc role wired with deploy_policy_json)
# invokes `aws ssm send-command --document-name <this doc> --targets <wg ec2>
# --parameters s3_key=<key>` to roll a new dashboard binary onto the WireGuard
# host. The instance role already holds s3:GetObject on the artifact bucket,
# so the deploy role only needs to *invoke* the document, not grant data plane
# access to S3.
#
# Both resources are count-gated on var.target_instance_arn so the module is
# a no-op for callers that haven't wired the target instance in yet.

resource "aws_ssm_document" "deploy" {
  count = var.target_instance_arn != null ? 1 : 0

  name            = "tf-${var.project_name}-${var.env}-dashboard-deploy"
  document_type   = "Command"
  document_format = "JSON"

  content = jsonencode({
    schemaVersion = "2.2"
    description   = "Pull the latest dashboard binary from S3 and restart wireguard-dashboard.service. Atomic-mv so a partial download can't leave a half-written binary at the live path."
    parameters = {
      # SSM document parameter names must be strictly alpha-numeric (no
      # underscores or hyphens), so we use camelCase here even though the
      # rest of the codebase is snake_case. Documented gotcha.
      s3Key = {
        type        = "String"
        description = "S3 key relative to the artifact bucket (e.g. `main-<sha>/wireguard-dashboard` or `latest/wireguard-dashboard`)."
        default     = "latest/wireguard-dashboard"
        # Restrict to the two prefix patterns the build workflow produces — defense-in-depth even though IAM also scopes the bucket.
        allowedPattern = "^(latest|main-[0-9a-f]+)/wireguard-dashboard$"
      }
    }
    mainSteps = [
      {
        action = "aws:runShellScript"
        name   = "deployDashboard"
        inputs = {
          runCommand = [
            # SSM RunShellScript executes with /bin/sh (dash on Ubuntu), not
            # bash. `pipefail` is a bash-ism that dash rejects with "Illegal
            # option -o pipefail". The script has no pipelines, so we only
            # need errexit (-e) and nounset (-u).
            "set -eu",
            "TMP=/opt/wireguard-dashboard/bin/wireguard-dashboard.new",
            "DEST=/opt/wireguard-dashboard/bin/wireguard-dashboard",
            # The instance role already grants s3:GetObject on the artifact bucket via Slice 1 sub-task 3.
            "aws s3 cp s3://${aws_s3_bucket.this.bucket}/{{ s3Key }} \"$TMP\"",
            "chmod +x \"$TMP\"",
            "mv \"$TMP\" \"$DEST\"",
            "systemctl restart wireguard-dashboard.service",
            "sleep 3",
            "systemctl is-active wireguard-dashboard.service",
            # Sanity check via the in-VPN URL — the dashboard binds to 172.16.15.1; this curl runs from within the EC2's network namespace and DOES reach the listener (the binary is local). Use --max-time so we fail fast if the bind never came up.
            "curl --silent --show-error --fail --max-time 5 http://172.16.15.1:8080/api/health"
          ]
        }
      }
    ]
  })

  tags = var.tags
}

data "aws_iam_policy_document" "deploy" {
  count = var.target_instance_arn != null ? 1 : 0

  statement {
    sid     = "SendCommandTargeted"
    actions = ["ssm:SendCommand"]
    resources = [
      aws_ssm_document.deploy[0].arn,
      var.target_instance_arn,
    ]
  }

  # Allow GetCommandInvocation so the workflow can poll the command status.
  # Scoping by command-id requires SSM-side knowledge the role doesn't have at
  # policy-eval time, so we allow the action account-wide — the workflow only
  # ever has command-ids it itself created.
  statement {
    sid       = "GetCommandInvocation"
    actions   = ["ssm:GetCommandInvocation"]
    resources = ["*"]
  }

  # The deploy workflow resolves the target instance ID at runtime by tag
  # (Name=wireguard-test) rather than hardcoding it, because Terraform may
  # replace the EC2 on user-data changes. ec2:DescribeInstances does not
  # support resource-level permissions per the AWS IAM action reference, so
  # Resource must be "*".
  statement {
    sid       = "DescribeInstancesForTagLookup"
    actions   = ["ec2:DescribeInstances"]
    resources = ["*"]
  }
}
