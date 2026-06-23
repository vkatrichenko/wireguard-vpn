---
name: prd-initializer
description: Generate a PRD.md document in the project root by analyzing the current codebase. Use when the user asks to create, initialize, or update the Product Requirements Document.
---

# Goal
Produce a comprehensive `PRD.md` file in the project root that accurately reflects the product requirements based on the actual codebase, without hallucinating business logic or inventing missing information.

# Instructions

1. Generate and Follow an Implementation Plan:
   - Before any execution, create a step-by-step plan for how you will gather context and generate the PRD.
   - State the plan explicitly and follow it sequentially.

2. Copy the Exact Skeleton:
   - Read the reference skeleton from `.agents/skills/prd-initializer/references/prd_reference.md`.
   - Copy this skeleton exactly to `PRD.md` in the project root.
   - Do NOT add, remove, or modify any structural sections. The structure must remain identical to the reference.

3. Use context-router.md as Primary Source of Truth:
   - Read `context-router.md` first to understand the project context map.
   - Follow the routes defined in `context-router.md` to gather relevant information.
   - Use `project-context/config/files_tree.md` as the primary map to locate specific features and components.
   - Use `project-context/product/` directory for product-level context.

4. Gather Codebase Information:
   - Explore the actual source code to understand implemented features.
   - Read relevant files from `src/` directory to identify user-facing capabilities.
   - Check `project-context/product/` documentation for feature specifications.

5. Translate Technical Details into Product Language:
   - Convert code-level implementations into user scenarios and features.
   - Describe what users can do, not how the code works.
   - Focus on product value and user outcomes.

6. Fill Each Section with Codebase-Derived Content:
   - Product Overview: Extract problem statement, objectives, and value proposition from the codebase and product documentation.
   - Target Audience: Identify user types from product documentation.
   - Behaviours: List product behaviors that support user stories.
   - Design & UX: Reference any design assets and specify responsive requirements found in codebase.
   - Technical Details: Reference context-router.md for technical architecture.
   - Non-Functional Requirements: Extract browser support, performance metrics from codebase configuration.
   - Analytics & Tracking: List any analytics events found in codebase.
   - Out of Scope: Identify features explicitly not implemented.
   - Milestones & Timeline: Document development phases if available.

7. Handle Missing Information Strictly:
   - If any required information is not available in the codebase or documentation, leave the field empty.
   - Alternatively, insert the exact marker: `[REQUIRES PRODUCT FILLING: no data in the code]`
   - NEVER invent, assume, or hallucinate any business logic or requirements.

8. Verify Before Finalizing:
   - Ensure all sections from the skeleton are present and unmodified.
   - Confirm no invented information exists in the document.
   - Check that technical details are translated to product language.

# Examples

User request: "Initialize PRD for this project."
Agent action:
1. Create implementation plan: (a) Read context-router.md, (b) Read product docs, (c) Explore codebase, (d) Copy skeleton, (e) Fill sections.
2. Read `.agents/skills/prd-initializer/references/prd_reference.md`.
3. Copy skeleton to `PRD.md`.
4. Gather context from codebase and product documentation.
5. Fill each section with verified, codebase-derived content.
6. Use `[REQUIRES PRODUCT FILLING: no data in the code]` for missing data.

User request: "Update PRD after new feature implementation."
Agent action:
1. Read existing `PRD.md`.
2. Analyze new codebase changes.
3. Update relevant sections with new product capabilities.
4. Maintain exact skeleton structure.
5. Do not invent any missing details.

# Constraints
- MUST generate and follow an implementation plan before execution.
- MUST use `context-router.md` as the primary source of truth for context gathering.
- MUST copy the exact skeleton from `.agents/skills/prd-initializer/references/prd_reference.md` without structural modifications.
- MUST translate all technical details into product-level user scenarios.
- MUST NOT invent, assume, or hallucinate any business logic or requirements.
- MUST leave empty or use `[REQUIRES PRODUCT FILLING: no data in the project]` for missing information.
- MUST NOT add, remove, or modify any structural sections from the skeleton.
- Do not use bold formatting in the PRD.md file.
- Do not use table formatting in the PRD.md file.
- Keep the PRD.md file clear and concise.
- All skill documentation and output must be written in English.