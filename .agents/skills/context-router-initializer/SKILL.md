---
name: context-router-initializer
description: Use this skill to initialize project context documentation by adapting the generic context-router.md template in the ROOT and generating route README documentation inside project-context/ for the user's specific project.
---

# CONTEXT ROUTER INITIALIZATION SKILL

You have been triggered to adapt the generic `context-router.md` template located in the project ROOT into a fully functional, project-specific behavioral rulebook.

Follow these steps strictly in order:

## Step 1: Analyze the Project Context
Before modifying the file, you must understand what this project is.
1. Read `README.md`, `package.json` (or equivalent dependency file), and scan the root directory.
2. Identify the core product vision, target audience, and main technology stack.
3. Identify 2 to 4 primary Business Routes (e.g., Users, Products, Tasks, Auth, Cart).

## Step 2: Update Project Info & Rules
Open `context-router.md` in the ROOT and replace the placeholders:
1. "Project Description": Replace `[Insert brief description...]` with a concise summary of the app's purpose and tech stack.
2. "Project Specific Rule": Replace `[PROJECT SPECIFIC RULE]` with 1-2 critical, non-negotiable constraints based on your project analysis (e.g., "All state mutations must use Redux", "Never use local storage for sensitive data"). If unsure, write a generic best-practice rule for the detected stack and ask the user to refine it later.
3. "PRD Path": Locate the project's PRD (e.g., `PRD.md`). Replace `[Insert path to your PRD...]` with the actual path.
4. "Project Terminology": Refine the Application vs Agent definitions in the `Project terminology` section to match the project's stack (e.g., "Application" might be a frontend SPA, a backend service, or a CLI tool).

## Step 3: Define Core Business Routes
Transform the generic `[Route X Name]` sections into actual routes:
1. Replace the placeholders with the core routes you identified in Step 1.
2. Write a brief 1-sentence description of the business logic each route governs.
3. Assign a valid directory path for each (e.g., `project-context/routes/users/`). DIRECTORY PATH RULE: Every `Directory Path` MUST be relative to the repository root, never relative to the current file. This applies to context-router.md AND to every sub-router README.md that declares child routes.
4. If these directories do not exist in the file system, CREATE them now.

## Step 4: Populate Route Documentation
For each route directory created in Step 3, generate a `README.md` using the template at `templates/route-readme-template.md` (relative to this skill directory).
1. Copy the template structure for each route.
2. Replace all `[...]` placeholders with project-specific content derived from your Step 1 analysis:
   - Route Name: the actual route name (e.g., "Tasks", "Users", "Auth").
   - Purpose: a concise explanation of what the route owns and why it exists.
   - Core Concepts: list the key entities, states, or workflows the route manages based on your codebase scan.
   - Invariants: write 1-3 real non-negotiable rules you observed or inferred for this route.
   - Route-Specific Constraints: add any data format, API boundary, or validation rules relevant to this route.
3. Keep each README short and factual. Avoid speculative content; only document what you can confirm from the codebase.
4. If a `README.md` already exists in a route directory, do NOT overwrite it.

Also ensure the `project-context/config/` directory exists. If `project-context/config/files_tree.md` does not exist, create it with a placeholder heading `# Project File Tree` so the path referenced by the router is valid.

## Step 5: Final Cleanup & Validation
1. Remove any remaining `[...]` placeholders from `context-router.md`.
2. Ensure there is absolutely NO "bold formatting" anywhere in `context-router.md`.
3. Verify every `Directory Path` in `context-router.md` AND in sub-router README.md files has a matching directory with a `README.md` on disk, and is relative to the repository root (not relative to the current file).
4. Save all files.

## Step 6: Completion
Report back to the user with a brief summary of:
- The routes identified and initialized.
- The route READMEs created (list file paths).
- Ask if they want to add any specific `Mandatory Execution Rules` or refine route documentation.

# Scaling Guidelines

## When to split a route into child routes
- The route's README.md exceeds ~100 lines because it documents multiple distinct rule sets.
- Two sub-areas have invariants that are independent or contradictory.
- A sub-area has its own lifecycle (e.g., can be created/modified/deleted independently of the parent route's rules).

## When NOT to split
- The route is small and all rules fit in one readable README.md.
- A child route would have only 1-2 invariants with no potential for further growth.
- The split would create routes that always need to be read together anyway.

## The fractal contract
Every sub-router (README.md) at any depth follows the same structure: Purpose, Core Concepts, Invariants, Constraints, and optional Child Routes. This is what makes the system scale infinitely — there is no special format for depth 1 vs depth 5. The root context-router.md never needs to change when a route splits into children; the split happens inside the sub-router.

## Where growth happens
- context-router.md (root) stays concise: only top-level routes. It rarely changes.
- Sub-routers absorb complexity: when a feature grows, its sub-router declares child routes.
- Leaf routes stay simple: no Child Routes section, just business rules.

# Terminology Reference

When writing or updating documentation, use these terms consistently:
- Context Router: The root `context-router.md` file providing a high-level map of the entire project.
- Route: A specific directory representing a business or technical module. Contains a README.md sub-router.
- Sub-router: The `README.md` file inside any route directory. Acts like the root context router but scoped to its route. Can declare child routes, creating infinite depth.
- Hierarchical routing: Router -> Route (Dir) -> Sub-router (README.md) -> Child Route (Dir) -> Child Sub-router (README.md), infinitely recursive.

# Reference Structure

## context-router.md required sections (in order)

```
# context-router.md Purpose
  - Root router / compass definition
  - "Implementation is the map, router is the compass"
  - Trigger rule

# context-router.md terminology
  - Context Router  (this root file)
  - Route           (directory = business/technical module node)
  - Sub-router      (README.md inside a route directory)

# Hierarchical routing principle
  - Recursive tree, no depth limit
  - Router -> Route (Dir) -> Sub-router (README.md) -> Child Route -> ...

# Project terminology
  - Application vs Agent/Copilot/LLM definitions

# Mandatory Execution Rules
  - Start from this file
  - Never hallucinate — read sub-routers
  - Traverse recursively
  - [PROJECT SPECIFIC RULE]
  - Load selectively
  - UPDATE TRIGGER (CRITICAL)

# Project context map (Routes)
  ## PRD & Vision
  ## Project description

# Core Business Routes (Behavioral Rules)
  ## Route A
     Directory Path: project-context/routes/route-a/
  ## Route B
     Directory Path: project-context/routes/route-b/
  ## AI Skills and Agents
     Directory Path: .agents/skills/

# Rules for this file (context-router.md)
```

## File system structure produced by context-router

```
project-root/
|-- context-router.md              <- Root Router (compass)
|-- PRD.md                         <- Product vision source of truth
|
|-- project-context/
|   |-- config/
|   |   |-- files_tree.md          <- WHERE files are (the map)
|   |
|   |-- routes/
|       |-- route-a/
|       |   |-- README.md          <- Sub-router for Route A
|       |   |-- child-route-1/
|       |   |   |-- README.md      <- Child sub-router (same contract)
|       |   |-- child-route-2/
|       |       |-- README.md
|       |
|       |-- route-b/
|       |   |-- README.md          <- Sub-router for Route B
|       |
|       |-- route-c/
|           |-- README.md          <- Sub-router for Route C
|
|-- .agents/
    |-- skills/                    <- AI Skills and Agents route
        |-- context-router-actualizer/
        |-- context-router-initializer/
```

## Directory Path rule in sub-routers

All Directory Paths MUST be relative to the repository root. This applies equally inside context-router.md and inside any sub-router README.md.

In context-router.md (root router):
```
## Route A
Directory Path: project-context/routes/route-a/
```

In project-context/routes/route-a/README.md (sub-router declaring child routes):
```
## Child Route 1
Directory Path: project-context/routes/route-a/child-route-1/

## Child Route 2
Directory Path: project-context/routes/route-a/child-route-2/
```

NEVER use relative paths like `Directory Path: child-route-1/` in a sub-router.

## Traversal flow (how an agent reads context)

```
1. Agent receives task
        |
        v
2. Read context-router.md (root router)
        |
        v
3. Identify which route(s) the task touches
        |
        v
4. Navigate to route directory -> Read README.md (sub-router)
        |
        v
5. Sub-router declares child routes? --YES--> Read child README.md
        |                                          |
        NO                                         v
        |                                    Repeat until leaf
        v
6. Agent has full context -> Execute task
```

# Examples

- See the actualizer skill's `examples/context-router-taskpilot.md` for a fully realized context-router from a production project. Use as reference for real-world route naming, project-specific rules, and proper structure.
- See `examples/context-router-example/context-router.md` for a generic template with placeholders.
- See the actualizer skill's `examples/scaling-example.md` for a step-by-step demonstration of how routing scales from tiny (2 routes) to huge (3+ levels deep) without changing any structural rules.