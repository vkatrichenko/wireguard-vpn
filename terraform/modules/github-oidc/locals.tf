locals {
  arn = (
    var.use_existing
    ? data.aws_iam_openid_connect_provider.github[0].arn
    : aws_iam_openid_connect_provider.github[0].arn
  )
  url = "https://token.actions.githubusercontent.com"
}
