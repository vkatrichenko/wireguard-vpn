# Account-wide GitHub Actions OIDC identity provider.
#
# IAM only allows one OIDC provider per issuer URL per AWS account, so this
# module owns the singleton. Other modules (dashboard, scheduled-job runners,
# future CI consumers) take its ARN as an input and reference it from their
# own IAM role trust policies — they do not create their own provider.
#
# Thumbprint is GitHub's CA per AWS docs. Note that for well-known IdPs
# (GitHub included), AWS validates against its own trusted-CA library and
# the thumbprint is retained for config but not used at runtime.

resource "aws_iam_openid_connect_provider" "github" {
  count = var.use_existing ? 0 : 1

  url             = "https://token.actions.githubusercontent.com"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = ["6938fd4d98bab03faadb97b34396831e3780aea1"]

  tags = merge(var.tags, {
    Name = "github-actions"
  })
}

data "aws_iam_openid_connect_provider" "github" {
  count = var.use_existing ? 1 : 0

  url = "https://token.actions.githubusercontent.com"
}
