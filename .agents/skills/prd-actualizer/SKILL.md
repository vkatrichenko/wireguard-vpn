---
name: prd-actualizer
description: Update an existing PRD.md to reflect the current codebase state by analyzing implemented features and synchronizing documentation with reality. Use when the user asks to actualize, update, sync, or refresh the Product Requirements Document.
---

# Goal
Synchronize an existing PRD.md file with the current codebase implementation. Ensure the documentation accurately reflects what is actually built from a business and user perspective, without hallucinating features or cluttering the document with long technical details.

# Instructions

1. Generate and Follow an Implementation Plan:
   - Before any execution, create a step-by-step plan for how you will actualize the PRD.
   - State the plan explicitly and follow it sequentially.

2. Verify PRD.md Exists:
   - Check that PRD.md exists in the project root.
   - If PRD.md does not exist, inform the user to run prd-initializer first.
   - Do NOT create a new PRD.md - that is the responsibility of prd-initializer.

3. Read Existing PRD.md:
   - Read the current PRD.md in its entirety.
   - Document which sections have content versus which are empty or contain placeholder markers.
   - Preserve all existing valid content that accurately reflects the codebase.

4. Apply PRD Reference Standards:
   - You MUST read and strictly follow `.agents/skills/prd-actualizer/references/prd_reference.md`.
   - Use this reference document as the definitive guide for the required skeleton, format, and content expectations.
   - Ensure all updates align perfectly with the guidelines specified in this reference.

5. Use context-router.md as Primary Source of Truth:
   - Read `context-router.md` first to understand the project context map.
   - Follow the routes defined in `context-router.md` to gather relevant information.
   - Use `project-context/config/files_tree.md` as the primary map to locate specific features and components.
   - Use `project-context/product/` directory for product-level context.

6. Analyze Current Codebase State:
   - Explore the actual source code to understand currently implemented features.
   - Read relevant files from `src/` directory to identify user-facing capabilities.
   - Check `project-context/product/` documentation for feature specifications.
   - Compare implemented features against what is documented in PRD.md.

7. Maintain a Product-Centric Focus:
   - PRD.md is a product document, not a technical specification.
   - Focus on "what" the product does and the value it brings to the user, rather than "how" it is implemented under the hood.
   - Summarize technical constraints or architecture ONLY if they directly impact the user experience or business logic.
   - Strictly avoid long technical details, deep architectural explanations, or raw code snippets.

8. Identify Discrepancies:
   - Document features implemented in code but missing from PRD.md.
   - Document features described in PRD.md but not found in codebase.
   - Identify sections with outdated or inaccurate information.

9. Update PRD.md Sections with Verified Content:
   - Product Overview: Update problem statement, objectives, and value proposition based on current implementation.
   - Target Audience: Verify and update user types from product documentation.
   - Behaviours: Update product behaviors that support user stories.
   - Design & UX: Update design assets and responsive requirements found in codebase.
   - Technical Details: Keep this extremely brief. Reference context-router.md for technical architecture instead of writing it out. Focus only on technical requirements that matter to product stakeholders (e.g., third-party integrations).
   - Non-Functional Requirements: Update browser support, performance metrics from codebase configuration in a high-level format.
   - Analytics & Tracking: Update any analytics events found in codebase.
   - Out of Scope: Update features explicitly not implemented.
   - Milestones & Timeline: Update development phases if available.

10. Handle Missing Information Strictly:
   - If any required information is not available in the codebase or documentation, leave the field empty.
   - Alternatively, insert the exact marker: `[REQUIRES PRODUCT FILLING: no data in the code]`
   - NEVER invent, assume, or hallucinate any business logic or requirements.
   - If a section was empty before and still has no data, keep it empty - do not add placeholder text.

11. Maintain Skeleton Structure:
   - Preserve all section headings from the original skeleton and `.agents/skills/prd-actualizer/references/prd_reference.md`.
   - Do NOT add, remove, or modify any structural sections.
   - Keep the exact format and ordering of sections.

12. Verify Before Finalizing:
   - Ensure all sections from the skeleton are present and unmodified.
   - Confirm no invented information exists in the document.
   - Check that technical details are translated to product language and kept concise.
   - Verify all updates are derived from actual codebase analysis.

# Examples

User request: "Actualize PRD for this project."
Agent action:
1. Create implementation plan: (a) Verify PRD exists, (b) Read PRD & references, (c) Gather context, (d) Analyze codebase, (e) Identify discrepancies, (f) Update sections maintaining product focus.
2. Verify PRD.md exists in project root.
3. Read existing PRD.md content and `prd_reference.md`.
4. Gather context from context-router.md and product documentation.
5. Analyze source code for implemented features.
6. Translate codebase findings into user-facing features, intentionally omitting deep code logic.
7. Update PRD.md sections with verified, codebase-derived content.
8. Use `[REQUIRES PRODUCT FILLING: no data in the code]` for missing data.

User request: "Refresh PRD to match current state."
Agent action:
1. Read existing PRD.md and baseline it against `prd_reference.md`.
2. Scan codebase for current feature set.
3. Update inaccurate sections with correct product-level information.
4. If a complex technical refactoring occurred, only document its impact on performance or user experience, avoiding the technical details of the refactoring itself.
5. Preserve skeleton structure throughout.

# Constraints
- MUST generate and follow an implementation plan before execution.
- MUST verify PRD.md exists before proceeding; if missing, direct user to run prd-initializer first.
- MUST strictly use and follow `.agents/skills/prd-actualizer/references/prd_reference.md` for PRD structure, format, and actualization guidelines.
- MUST use `context-router.md` as the primary source of truth for context gathering.
- MUST preserve the exact skeleton structure from the original PRD.md and reference.
- MUST NOT include long technical details, deep architectural explanations, or raw code snippets.
- MUST translate all technical details into product-level user scenarios.
- MUST NOT invent, assume, or hallucinate any business logic or requirements.
- MUST leave empty or use `[REQUIRES PRODUCT FILLING: no data in the project]` for missing information.
- MUST NOT add, remove, or modify any structural sections from the skeleton.
- MUST NOT create a new PRD.md - only update existing ones.
- Do not use bold formatting in the PRD.md file.
- Do not use table formatting in the PRD.md file.
- Keep the PRD.md file clear, concise, and focused on business value.
- All skill documentation and output must be written in English.