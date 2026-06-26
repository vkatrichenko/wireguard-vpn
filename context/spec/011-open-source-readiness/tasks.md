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

- [ ] **Slice 4: Config & gitignore hygiene**
  - [ ] Edit `.claude/settings.json` — remove `permissions.allow: ["Bash", "Write"]`; keep the `extraKnownMarketplaces` (awos) entry. **[Agent: devsecops-quality]**
  - [ ] Edit `.gitignore` — add `*.mmdb` and `*.tfplan`. **[Agent: devsecops-quality]**
  - [ ] Verify: committed `settings.json` has no broad `allow` and is valid JSON; `git check-ignore` confirms `*.mmdb` and `*.tfplan` are ignored; `settings.local.json` still ignored; `make pre-commit` passes. **[Agent: devsecops-quality]**

- [ ] **Slice 5: Git-history rewrite — purge GeoLite2 blob (OWNER-RUN)**
  - [ ] Produce a runbook with the exact BFG commands: mirror clone → `java -jar bfg.jar --delete-files GeoLite2-City.mmdb` → `git reflog expire --expire=now --all && git gc --prune=now --aggressive` → verify → `git push --force`. **[Agent: general-purpose]**
  - [ ] **Owner** executes the rewrite + `git push --force`, then confirms `git rev-list --objects --all | grep -i mmdb` is empty and the `.git` directory shrank materially. **(Owner-run — the agent cannot execute or verify this slice; force-push to the public remote is the owner's, per the never-push-without-confirmation rule.)**

---

## Notes & accepted exceptions

| Task/Slice | Issue | Resolution |
|------------|-------|------------|
| Slice 1 & 2 doc sub-tasks | Assigned to `general-purpose` — no legal/governance-docs specialist | Accepted (prose/legal files); a `docs-writer` agent could be added later |
| Slice 5 (history rewrite) | Cannot be agent-executed/verified — owner-run force-push to a public remote | **Approved** to skip agent verification; owner runs the runbook + force-push and confirms the verification commands |
| GitHub-render checks (Slices 1–3) | License sidebar / Security tab / template rendering only verifiable on GitHub | Owner confirms post-merge on GitHub; local agent verification covers file presence/validity |

## Out of scope (recorded)

- Purging `terraform/dev/tfplan` from history and rotating the WireGuard server key — deliberately deferred per owner decision (2026-06-26); see functional-spec §3.
