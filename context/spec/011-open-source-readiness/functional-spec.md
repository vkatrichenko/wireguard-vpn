# Functional Specification: Open-Source Readiness

- **Roadmap Item:** "Repository open-sourcing" (promoting from _Future / Under Consideration_)
- **Status:** Completed
- **Author:** Vladyslav Katrychenko

---

## 1. Overview and Rationale (The "Why")

The repo is solo-maintained today but intended to be published publicly. Before it can go public it needs the standard governance, legal, and hygiene artifacts that let external readers understand how to **use, trust, contribute to, and report problems against** the project — and it must not ship oversized or over-permissioned baggage.

Today there is no license (so legally no one may reuse it), no contribution or security-reporting guidance, no issue/PR scaffolding, a ~65 MB stale GeoIP database bloating clone size, and a committed agent-permission file that auto-approves broad actions for anyone who clones it.

This change adds the missing governance/legal/community files and tightens the committed agent permissions — so the repository is safe, legible, and welcoming to publish. _(The git-history purge originally planned here was later descoped by owner decision — see §2.5.)_

**Success looks like:** a fresh public clone has a clear license + contribution/security guidance + issue/PR templates, and carries no broad auto-approve permissions.

**Non-goal:** no change to VPN/dashboard runtime behavior or infrastructure. This is a governance/hygiene change only.

---

## 2. Functional Requirements (The "What")

### 2.1 License (Apache-2.0)

- Add a top-level `LICENSE` (Apache-2.0) and a `NOTICE` / `THIRD-PARTY-NOTICES` file enumerating bundled third-party assets and their licenses: IBM Plex Sans/Mono (SIL OFL-1.1), DB-IP IP-to-City Lite (CC BY 4.0), htmx, Chart.js (+ chartjs-adapter-date-fns), modernc.org/sqlite, oschwald/geoip2-golang, and any other vendored assets — reconciled with the existing `dashboard/web/static/fonts/VENDORED.txt`.
- Update the README "License" section (currently honest-about-missing) to reference Apache-2.0.
- Copyright holder: **Vladyslav Katrychenko**.
  - **Acceptance Criteria:**
    - [x] A root `LICENSE` file contains the standard Apache-2.0 text with the correct copyright holder and year.
    - [x] A `NOTICE` (or `THIRD-PARTY-NOTICES`) file lists every bundled third-party asset with its license and attribution; it is consistent with `dashboard/web/static/VENDORED.txt`, `fonts/OFL.txt`, and `internal/geoip/LICENSE-DB-IP.txt`.
    - [x] The README "License" section references Apache-2.0 (no longer states a license is missing).

### 2.2 SECURITY.md

- Add a `SECURITY.md` describing how to report a vulnerability via **GitHub private security advisories** ("Report a vulnerability"), a supported-versions note, and response expectations. No public email is required or exposed.
  - **Acceptance Criteria:**
    - [x] `SECURITY.md` is present (root or `.github/`) and directs reporters to GitHub private advisories.
    - [x] It notes that the operator must enable "Private vulnerability reporting" in the repository settings for the flow to work.
    - [x] It states a supported-versions / response-time expectation.

### 2.3 CONTRIBUTING.md

- Add a `CONTRIBUTING.md` covering: local dev setup (Terraform + the Go dashboard), the `make pre-commit` quality gate, Conventional Commits + branch-naming conventions, the awos `spec → tech → tasks → implement` workflow, the Terraform conventions (exact version pinning, the four required tags, `locals.tf` over `terraform.tfvars`, the `plan -out` → review → `apply` workflow), and the no-CI / local-apply-only reality.
  - **Acceptance Criteria:**
    - [x] `CONTRIBUTING.md` is present and documents dev setup, the `make pre-commit` gate, commit/branch conventions, the awos workflow, and the Terraform conventions.
    - [x] It states that there is no CI and that Terraform apply is manual/local, run only by the owner.

### 2.4 Issue & PR templates

- Add GitHub issue templates (a bug report and a feature request) and a pull-request template. The PR template mirrors the project's PR-description convention (Summary / Architecture decisions / surface-area table).
  - **Acceptance Criteria:**
    - [x] `.github/ISSUE_TEMPLATE/` contains a bug-report and a feature-request template.
    - [x] A `PULL_REQUEST_TEMPLATE.md` exists and follows the project's PR-description structure (Summary, optional Architecture decisions, a surface-area table).

### 2.5 Git-history hygiene (mmdb only) — DESCOPED (2026-06-26)

- **Descoped by owner decision (2026-06-26): the history rewrite will not be performed.** The stale `dashboard/internal/geoip/GeoLite2-City.mmdb` (~65 MB) remains in git history. The destructive force-push needed to rewrite already-published history was judged not worth the disruption; the forward-looking `.gitignore` hardening shipped in §2.6 (which now ignores `*.mmdb`, `*.tfplan`, and `tfplan`) prevents any future GeoIP-db or plan-file commit and was deemed sufficient.
  - **Acceptance Criteria:** _(not applicable — requirement descoped)_
    - [ ] ~~`git rev-list --objects --all` references no `.mmdb` path after the rewrite.~~ _(descoped)_
    - [ ] ~~The `.git` directory size drops materially (the 65 MB blob is gone from history).~~ _(descoped)_
    - [ ] ~~The working tree is unchanged; only history is rewritten.~~ _(descoped)_
    - [ ] ~~The rewrite/force-push is documented.~~ _(descoped)_

### 2.6 Permissions hygiene

- Scope down the committed `.claude/settings.json`: remove the blanket `["Bash", "Write"]` allow; keep the awos marketplace entry; ensure `settings.local.json` is gitignored and document that personal/permissive permissions belong there.
  - **Acceptance Criteria:**
    - [x] The committed `.claude/settings.json` carries no broad auto-approve `allow` (no blanket `Bash`/`Write`).
    - [x] `.claude/settings.local.json` is gitignored (verified or added).
    - [x] A short note (in `CONTRIBUTING.md` and/or alongside the settings) explains where contributors place their own permissions.

---

## 3. Scope and Boundaries

### In-Scope

- Apache-2.0 `LICENSE` + `NOTICE`/attributions.
- `SECURITY.md` (GitHub private advisories).
- `CONTRIBUTING.md`.
- GitHub issue templates + PR template.
- `.claude/settings.json` scope-down + `.gitignore` hardening (`*.mmdb`, `*.tfplan`, `tfplan`).
- README "License" section update.

### Out-of-Scope

- **Purging the GeoLite2 `.mmdb` (~65 MB) from git history (§2.5).** Descoped by owner decision (2026-06-26); the blob remains in history, and the `.gitignore` hardening prevents future re-commits.
- **Purging `terraform/dev/tfplan` from history and rotating the WireGuard server key.** _Noted residual risk:_ a saved Terraform plan in history likely embeds the rendered `user_data`, which includes the server private key read from SSM at plan time — and the repo already has PRs on GitHub. Per owner decision (2026-06-26), this is **deliberately deferred**; recorded here so the decision is on the record.
- `CODE_OF_CONDUCT.md` (may be added later).
- CI workflows / branch protection (separate effort; relates to the recurring `mergeable_state: blocked` on PRs).
- **Spec B — Alerting & observability** (Telegram/Discord/email transports, Prometheus `/metrics`, removing the peer-down alert) — separate specification.
- **Spec C — ARM option** (Graviton arm64 instance + arm64 dashboard build) — separate specification.
- Any VPN or dashboard runtime behavior, or any infrastructure resource change.
