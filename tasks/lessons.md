# Lessons

Append-only log of corrections from the repo owner. Each entry captures a mistake and the rule that prevents it next time. Review this file at session start.

## Entry template

```
### YYYY-MM-DD — <short title>

**Context:** what I was doing / what I suggested.
**Correction:** what the owner said.
**Rule:** the explicit behavior change. Phrased as "always X" or "never Y".
**Why it matters:** the reason — often a past incident or strong preference.
```

---

<!-- Newest entries on top. Example:

### 2026-04-21 — Don't resolve AMIs via most_recent

**Context:** suggested `data "aws_ami"` with `most_recent = true` for Ubuntu lookup.
**Correction:** owner wants AMIs pinned explicitly in locals.tf so rotation is a reviewable commit.
**Rule:** Always pin AMI IDs in `locals.tf`. Never use `most_recent = true` in AMI data sources for this repo.
**Why it matters:** non-deterministic AMI resolution makes two engineers' applies diverge and hides EC2 replacements behind "latest."

-->
