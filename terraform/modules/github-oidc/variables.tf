variable "tags" {
  description = "A map of tags to assign to resources."
  type        = map(string)
}

variable "use_existing" {
  description = "When true, the module reads the existing GitHub Actions OIDC provider from the account via a data source instead of creating a new one. Use this when the AWS account already has a `token.actions.githubusercontent.com` OIDC provider registered (e.g. by a sibling repo or a previous Terraform run). When false (default), the module creates the provider."
  type        = bool
  default     = false
}

variable "roles" {
  description = "Map of IAM role definitions keyed by role short-name. Each role gets a trust policy assumable via the GitHub OIDC provider, restricted to the supplied subject claim. Permissions are assembled from `s3_put_object` (typed shorthand for the common 'CI uploads to S3' case) and `inline_policy_json` (escape hatch for arbitrary statements). Both are optional; both apply when set; neither set yields a role with trust-only and no permissions."
  type = map(object({
    name_suffix = string
    subject     = string

    # Typed shorthand: a list of (bucket_arn, prefixes) entries. The module
    # builds a single policy statement per entry: actions=s3:PutObject,
    # resources = ["${bucket_arn}/${prefix}", ...].
    s3_put_object = optional(list(object({
      bucket_arn = string
      prefixes   = list(string)
    })), [])

    # Escape hatch: a raw IAM policy JSON string (typically the output of
    # data.aws_iam_policy_document.<name>.json) appended as additional
    # statements. Use when s3_put_object isn't expressive enough.
    inline_policy_json = optional(string, "")
  }))

  default = {}
}
