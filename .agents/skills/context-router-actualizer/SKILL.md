---
name: context-router-actualizer
description: Use this skill to actualize context-router.md routes and project-context/ documentation by diving into the project changes and updating behavioral rules, business logic, and architectural constraints.
---

# CONTEXT ROUTER ACTUALIZATION SKILL

You have been triggered to update the project's documentation because a recent task modified the fundamental business logic, data structures, or architectural rules of the application.

Your goal is to ensure the `project-context/` documentation and `context-router.md` accurately reflect the *current* behavioral rules of the system.

Follow these steps strictly:

## Step 1: Identify the Scope of Change
Analyze the changes you or the user just made to the codebase:
1. Did you modify an existing Route (e.g., added a new required field, changed how a module works)? -> Proceed to Step 2.
2. Did you introduce a completely NEW Route (e.g., a new business entity or technical module)? -> Proceed to Step 2, then Step 3.
3. Did you only refactor code, change UI styles, or fix a minor bug without changing business rules? -> STOP. No actualization is needed.

## Step 2: Actualize Route Documentation
Go to the specific route directory in `project-context/` (e.g., `project-context/routes/[route-name]/` or `project-context/routes/[route-name]/` depending on the project's convention). Create it if it is a new route.
1. Update or create the sub-router (`README.md`) detailing the NEW business rules, required elements, and constraints for this route.
2. FOCUS ON THE "WHY" AND "WHAT MUST BE DONE": Explain the structural rules and architectural decisions.
3. DO NOT DUPLICATE CODE: Do not paste function signatures, interface definitions, or line-by-line implementations. Code is the map; documentation is the compass.
4. If the route has child routes with their own sub-routers, traverse and update those as needed. Remember the hierarchical routing principle: the routing system is a recursive tree with no depth limit.
5. DIRECTORY PATH RULE: Every `Directory Path` in any router or sub-router MUST be relative to the repository root. Never use paths relative to the current file. Example: a child route inside `route-a` must be `Directory Path: project-context/routes/route-a/child-route/`, not `Directory Path: child-route/`.

## Step 3: Evaluate Context Router Impact (Only for NEW Routes)
Open `context-router.md` in the ROOT directory.
1. Rule Check: ONLY modify this file if you introduced a completely NEW distinct Route (business entity or technical module).
2. Action: If a new route was created, add a new entry under the `# Core Business Routes (Behavioral Rules)` section.
3. Format: Use the standard format:
   ```markdown
   ## [Route Name] Route
   [1-2 sentences explaining the rules governed by this route]
   Directory Path: project-context/routes/[route-name]/
   ```
4. Strict Prohibition: NEVER add routes for individual UI components, helper functions, or specific code files to context-router.md. Only top-level business entities or technical modules qualify.

## Step 4: Format & Constraints Check
Before finalizing your updates to `context-router.md`, verify:
- [ ] There is absolutely NO bold formatting anywhere in the context-router.md file.
- [ ] The file remains clear, concise, and top-level.
- [ ] You did not add files_tree.md paths as explicit routes.
- [ ] Every route listed has a matching directory with a README.md sub-router on disk.
- [ ] No orphaned files: every non-README.md file or subdirectory inside a route directory is declared as a child route in that route's sub-router (README.md). If an undeclared file or directory is found, either convert it into a proper child route (move into its own directory with a README.md) or remove it. Loose files break the routing contract because agents will never discover them through traversal.
- [ ] Every `Directory Path` (in context-router.md AND in sub-router README.md files) is relative to the repository root, not relative to the current file.
- [ ] The terminology section, hierarchical routing principle, and project terminology sections are intact and unmodified.

## Step 5: Completion
Provide a brief summary to the user outlining which route rules were updated and if any new routes were added to the context router.

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

- `examples/context-router-taskpilot.md` - A fully realized context-router from a production project (Python Slack bot with Jira integration). Use as reference for real-world route naming, project-specific rules, and proper structure.
- `examples/context-router-example/context-router.md` - A generic template with placeholders, matching the AWESOME structure. Use as the starting point for new projects.
- `examples/scaling-example.md` - Shows how the same routing system scales from a 2-route tiny project to a 3+ level deep project without changing any structural rules. Demonstrates the fractal contract, when to split, and when not to.