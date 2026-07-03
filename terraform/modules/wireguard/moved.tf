# Spec 020 slice 2: the instance policy/role/profile and their baseline
# attachments were de-`count`ed (previously `count = var.use_eip ? 1 : 0`), so
# their addresses lose the `[0]` index. These `moved` blocks tell Terraform the
# existing state objects were RENAMED, not replaced — a plain destroy/recreate
# would detach the instance profile from the live, running WireGuard instance
# (and drop its IAM identity mid-flight). The deployed environment runs
# use_eip=true, so every source address currently exists in state at `[0]` and
# these mappings resolve cleanly to the new unindexed addresses.
moved {
  from = aws_iam_policy.wireguard_policy[0]
  to   = aws_iam_policy.wireguard_policy
}

moved {
  from = aws_iam_role.wireguard_role[0]
  to   = aws_iam_role.wireguard_role
}

moved {
  from = aws_iam_role_policy_attachment.wireguard_roleattach[0]
  to   = aws_iam_role_policy_attachment.wireguard_roleattach
}

moved {
  from = aws_iam_role_policy_attachment.wireguard_ssm_core[0]
  to   = aws_iam_role_policy_attachment.wireguard_ssm_core
}

moved {
  from = aws_iam_instance_profile.wireguard_profile[0]
  to   = aws_iam_instance_profile.wireguard_profile
}
