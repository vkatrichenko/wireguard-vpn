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

### 2026-07-07 — Verify current state before filing a "add X" task

**Context:** asked to file a task for a Prometheus `/metrics` endpoint on the dashboard; I created Task #10 premised on it not existing, reusing a stale "captured decision" from a prior session summary (which also wrongly claimed disk usage wasn't exposed).
**Correction:** owner: "but dashboard already have metric endpoint http://172.16.15.1:8080/metrics" — it was already live (v0.0.17) and in source (`server.go` `GET /metrics` → `handleGetMetricsProm`, spec 012), exposing disk/cpu/mem/peers/alerts.
**Rule:** Before creating any task premised on something being missing/broken, VERIFY against live + source first (curl the endpoint, grep the code). Never trust a prior-session summary's "captured decisions" as ground truth — they reflect a past moment and may be wrong.
**Why it matters:** a task built on a false premise wastes the assignee's time and pollutes the board; the fix is one grep/curl away.

<!-- Newest entries on top. Example:

### 2026-04-21 — Don't resolve AMIs via most_recent

**Context:** suggested `data "aws_ami"` with `most_recent = true` for Ubuntu lookup.
**Correction:** owner wants AMIs pinned explicitly in locals.tf so rotation is a reviewable commit.
**Rule:** Always pin AMI IDs in `locals.tf`. Never use `most_recent = true` in AMI data sources for this repo.
**Why it matters:** non-deterministic AMI resolution makes two engineers' applies diverge and hides EC2 replacements behind "latest."

-->
