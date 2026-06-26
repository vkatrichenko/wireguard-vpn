# Tasks: Open-Source Readiness

- **Functional Specification:** [functional-spec.md](./functional-spec.md)
- **Technical Considerations:** [technical-considerations.md](./technical-considerations.md)

Each additive slice leaves the repo in a runnable state (`make pre-commit` stays green — no Terraform/code is touched). The git-history rewrite is isolated as an owner-run final slice.

---

- [x] **Slice 1: License & attribution**
  - [x] Add root `LICENSE` — verbatim Apache-2.0, `Copyright 2026 Vladyslav Katrychenko`. **[Agent: general-purpose]**
  - [x] Add root `NOTICE` enumerating bundled assets + licenses, reconciling `dashboard/web/static/VENDORED.txt`, `dashboard/web/static/fonts/OFL.txt`, `dashboard/internal/geoip/LICENSE-DB-IP.txt`, and `dashboard/go.mod`. **[Agent: general-purpose]**
  - [x] Update the README "License" section to reference Apache-2.0. **[Agent: general-purpose]**
  - [x] Verify: `LICENSE`/`NOTICE` present; Apache-2.0 text complete and unmodified; NOTICE lists IBM Plex (OFL-1.1), DB-IP (CC BY 4.0), htmx, Chart.js, modernc.org/sqlite, geoip2-golang, x/sys; README renders; `make pre-commit` passes. (GitHub license-sidebar detection is an owner post-merge check.) **[Agent: devsecops-quality]**

- [x] **Slice 2: Security & contribution docs**
  - [x] Add `SECURITY.md` — vulnerability reporting via GitHub private advisories, supported versions, response window; note the operator must enable "Private vulnerability reporting" in repo settings. **[Agent: devsecops-quality]**
  - [x] Add `CONTRIBUTING.md` — local dev setup (Terraform + dashboard), `make pre-commit`, Conventional Commits + branch naming, the awos workflow, Terraform conventions (exact pins, four tags, `locals.tf`, `plan -out` apply), the no-CI/local-apply reality, and where personal Claude perms live (`settings.local.json`). **[Agent: general-purpose]**
  - [x] Verify: both files present; SECURITY points to private advisories; CONTRIBUTING covers all required topics; markdown valid; `make pre-commit` passes. **[Agent: devsecops-quality]**

- [x] **Slice 3: GitHub issue & PR templates**
  - [x] Add `.github/ISSUE_TEMPLATE/bug_report.md` and `.github/ISSUE_TEMPLATE/feature_request.md`. **[Agent: cicd-github-actions]**
  - [x] Add `.github/PULL_REQUEST_TEMPLATE.md` mirroring the project PR convention (Summary / optional Architecture decisions / surface-area table). **[Agent: cicd-github-actions]**
  - [x] Verify: files present and valid; issue templates structurally correct; PR template matches the convention. (GitHub template rendering is an owner post-merge check.) **[Agent: cicd-github-actions]**

- [x] **Slice 4: Config & gitignore hygiene**
  - [x] Edit `.claude/settings.json` — remove `permissions.allow: ["Bash", "Write"]`; keep the `extraKnownMarketplaces` (awos) entry. **[Agent: devsecops-quality]** _(settings.json is harness-gated for subagents; the edit was applied by the lead agent.)_
  - [x] Edit `.gitignore` — add `*.mmdb` and `*.tfplan` (plus bare `tfplan` for the extensionless historical plan file). **[Agent: devsecops-quality]**
  - [x] Verify: committed `settings.json` has no broad `allow` and is valid JSON; `git check-ignore` confirms `*.mmdb` and `*.tfplan` are ignored; `settings.local.json` still ignored; `make pre-commit` passes. **[Agent: devsecops-quality]**

- [ ] ~~**Slice 5: Git-history rewrite — purge GeoLite2 blob (OWNER-RUN)**~~ — **DESCOPED (2026-06-26, owner decision: "no need to clean git history")**
  - The ~65 MB GeoLite2 blob remains in history; the destructive force-push was judged not worth the disruption. The `.gitignore` hardening from Slice 4 (`*.mmdb`, `*.tfplan`, `tfplan`) prevents future re-commits, which was deemed sufficient. The BFG runbook is recorded in `technical-considerations.md` §2.3 should this be revisited.

---

## Notes & accepted exceptions

| Task/Slice | Issue | Resolution |
|------------|-------|------------|
| Slice 1 & 2 doc sub-tasks | Assigned to `general-purpose` — no legal/governance-docs specialist | Accepted (prose/legal files); a `docs-writer` agent could be added later |
| Slice 5 (history rewrite) | Owner-run force-push to a public remote | **DESCOPED (2026-06-26)** — owner decided the history purge is not needed; `.gitignore` hardening covers future re-commits |
| GitHub-render checks (Slices 1–3) | License sidebar / Security tab / template rendering only verifiable on GitHub | Owner confirms post-merge on GitHub; local agent verification covers file presence/validity |

## Out of scope (recorded)

- Purging `terraform/dev/tfplan` from history and rotating the WireGuard server key — deliberately deferred per owner decision (2026-06-26); see functional-spec §3.
