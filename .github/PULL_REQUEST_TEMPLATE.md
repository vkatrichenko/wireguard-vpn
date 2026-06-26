<!--
PR description convention for this repo. Keep these headings after you fill
them in. Mirror the structure below; delete the HTML comments as you go.
-->

## Summary

<!--
Bullet list of the major changes. One concrete change per bullet, written in
the imperative and outcome-first ("Add X", "Replace Y with Z"). Group related
changes into a single bullet rather than one bullet per file.
-->

-
-

## Architecture decisions

<!--
Optional — include only when there's a non-obvious decision worth recording.
One short paragraph per decision, each with a bold lead-in naming it, e.g.:

  **OIDC over static keys.** Explain *why*, plus any trade-off or constraint.

Note any temporary or follow-up items inline with a date, e.g.:
"TEMPORARY (2026-06-26): … should be removed in a follow-up."

Delete this section if it doesn't apply.
-->

## Surface area

<!--
Pick the table shape that fits the change and delete the others:
  - File / component table (refactors touching many files)
  - Workflow / job table:  | Workflow | Trigger | Purpose |
  - Resource table:        | Resource | Purpose |
Mechanical, repeated changes (e.g. a rename across many files) get one row,
not one row per file.
-->

| File / Component | Change |
|------------------|--------|
|                  |        |

## Checklist

- [ ] Ran `make pre-commit` (fmt / docs / tflint / trivy) and it passes.
- [ ] Updated docs (README / CONTRIBUTING / specs) if behavior or setup changed.
- [ ] For Terraform changes: reviewed `terraform plan -out=tfplan` output and noted resource counts / any replacements in the description above.
- [ ] No secrets, private keys, or real IPs committed.
