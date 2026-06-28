# Technical Specification: Open-Source Readiness

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Status:** Completed
- **Author(s):** Vladyslav Katrychenko

---

## 1. High-Level Technical Approach

A pure repository-content change — **no application code, no Terraform resources, no runtime/infra behavior**. The work splits into two distinct workstreams:

- **(A) Additive files** — governance/legal/community Markdown, the verbatim Apache-2.0 text, and two small config edits. Safe; ships as a normal commit/PR.
- **(B) A one-off, destructive git-history rewrite** — drops the 65 MB stale GeoIP blob from all history. Owner-run and force-pushed, handled **separately** from the additive PR.

No new frameworks, services, or specialist capabilities are introduced.

---

## 2. Proposed Solution & Implementation Plan (The "How")

### 2.1 Governance & legal files (additive)

| Path | Responsibility |
|------|----------------|
| `LICENSE` (root) | Verbatim Apache-2.0 license text; `Copyright 2026 Vladyslav Katrychenko`. |
| `NOTICE` (root) | Attribution list reconciling the three existing attribution files (`dashboard/web/static/VENDORED.txt`, `dashboard/web/static/fonts/OFL.txt`, `dashboard/internal/geoip/LICENSE-DB-IP.txt`) plus `dashboard/go.mod`: IBM Plex Sans/Mono (SIL OFL-1.1), DB-IP IP-to-City Lite (CC BY 4.0), htmx, Chart.js (+ chartjs-adapter-date-fns), modernc.org/sqlite (BSD-3), oschwald/geoip2-golang (ISC), golang.org/x/sys (BSD-3). Points to the existing per-asset license files rather than duplicating their full texts. |
| `SECURITY.md` (root or `.github/`) | Vulnerability reporting via GitHub **private security advisories** ("Report a vulnerability"); supported-versions note; response-window expectation. Notes the operator must enable "Private vulnerability reporting" in repo settings for the flow to work. |
| `CONTRIBUTING.md` (root) | Local dev setup (Terraform + Go dashboard), the `make pre-commit` gate, Conventional Commits + branch naming, the awos `spec → tech → tasks → implement` workflow, the Terraform conventions (exact pins, four required tags, `locals.tf`, `plan -out` apply), the no-CI / local-apply-only reality, and where contributors place personal Claude permissions (`settings.local.json`). |
| `.github/ISSUE_TEMPLATE/bug_report.md` | Bug-report issue template. |
| `.github/ISSUE_TEMPLATE/feature_request.md` | Feature-request issue template. |
| `.github/PULL_REQUEST_TEMPLATE.md` | Mirrors the project's PR-description convention (Summary / optional Architecture decisions / surface-area table). |
| `README.md` (edit) | Update the "License" section to reference Apache-2.0 (currently honest-about-missing). |

Existing per-asset license files stay where they are; `NOTICE` references them rather than relocating or duplicating.

### 2.2 Config hygiene (additive edits)

- **`.claude/settings.json`** — remove `permissions.allow: ["Bash", "Write"]`; retain the `extraKnownMarketplaces` (awos) entry. `settings.local.json` is already gitignored (verified), so personal/permissive perms live there.
- **`.gitignore` hardening (forward-looking)** — add `*.mmdb` and `*.tfplan`. Today the current DB-IP db is ignored via a nested `geoip/.gitignore`, but the old `GeoLite2-City.mmdb` name and `tfplan` are **not** ignored at root — which is how they were committed historically. This change touches no history; it prevents any future GeoIP-db or plan-file commit (and so partially mitigates the deferred `tfplan` secret risk going forward).

### 2.3 Git-history rewrite (destructive, owner-run) — DESCOPED (2026-06-26)

> **Not implemented.** Per owner decision (2026-06-26) the history rewrite was descoped; the ~65 MB GeoLite2 blob remains in history. The plan below is retained for reference should the purge be revisited. The forward-looking `.gitignore` hardening (§2.2) shipped instead and prevents future GeoIP-db/plan-file commits.

- **Tool:** BFG Repo-Cleaner, run as a downloaded jar via the already-present `java` (no global install). _Alternative, if preferred: `git-filter-repo` (requires `brew`/`pip` install)._
- **Procedure** — Claude provides the exact commands; the **owner** executes the force-push (consistent with the "never push without explicit confirmation" rule and the owner running destructive git ops):
  1. `git clone --mirror <origin> wg.git`
  2. `java -jar bfg.jar --delete-files GeoLite2-City.mmdb wg.git`
  3. `cd wg.git && git reflog expire --expire=now --all && git gc --prune=now --aggressive`
  4. Verify: `git rev-list --objects --all | grep -i mmdb` → empty
  5. `git push --force` (owner-confirmed), then re-clone / hard-reset working copies and rebase or recreate any open PRs.
- **Sequencing:** ship the additive PR first (low risk), then perform the rewrite as a separate owner-run step so the new files aren't rebased on top of a rewrite.

_No API, data-model, or UI changes._

---

## 3. Impact and Risk Analysis

- **System Dependencies:** GitHub repository settings (enable private vulnerability reporting); the `java` runtime for BFG; the owner's git push access. No code/infra dependencies.
- **Potential Risks & Mitigations:**
  - **Force-push disrupts open PRs / existing clones.** → Owner-run on a solo repo; rebase/recreate PRs afterward; note the rewrite in the PR/commit.
  - **Best-effort server-side eradication.** → After a force-push, old SHAs, PR refs, and cached blob views may linger on GitHub. Acceptable here: the blob is **non-secret bloat**, so the goal (smaller clones for new cloners) is met. The deferred `tfplan` secret remains out of scope per owner decision.
  - **Wrong-file deletion during rewrite.** → Operate on a fresh mirror clone, verify `git rev-list` is clean **before** force-pushing, and keep a backup mirror until confirmed.
  - **Attribution incompleteness.** → Reconcile `NOTICE` against the three existing attribution files + `go.mod`; optionally run `go-licenses` to enumerate module licenses.
  - **Reduced auto-approve convenience for the owner.** → Personal permissions move to the gitignored `settings.local.json`.

---

## 4. Testing Strategy

No automated tests — this is a content/hygiene change. Verification steps:

- **Files present & well-formed:** Apache-2.0 text intact and detected in the GitHub repo sidebar; `SECURITY.md` surfaces a Security tab; issue/PR templates render on GitHub.
- **History rewrite:** `git rev-list --objects --all | grep -i mmdb` is empty; `du -sh .git` is materially smaller before → after.
- **gitignore:** `git check-ignore` confirms `*.mmdb` and `*.tfplan` are now ignored.
- **Permissions:** committed `.claude/settings.json` has no broad `allow`; `settings.local.json` still ignored.
- **No regressions:** `make pre-commit` still passes (no Terraform/code touched); the README "License" section renders correctly.
