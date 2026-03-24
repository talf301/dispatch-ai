---
name: dispatch-planner
description: Decompose an approved spec into parallel dt tasks with dependency wiring and a parent plan branch for merging completed work. Use in place of writing-plans when targeting dispatch for execution.
---

# Dispatch Planner

**Announce:** "Using dispatch-planner to decompose the spec into parallel tasks."

**Type:** Rigid — follow this process exactly.

## Flags

- `--auto` — Skip user approval gates (phases 2 and 3). Use best judgement for parallelism and task structure. The review loop (phase 4) still runs.

## Preconditions

- A spec document exists. If the path isn't clear from conversation context, ask for it.
- `dt` is on PATH. Verify with `dt --help`.
- **Do NOT use TaskCreate/TaskUpdate for dispatch tasks.** All task creation and dependency wiring must go through `dt batch`. The built-in task tools are for conversation-scoped tracking only.

## Process

### Phase 1: Analysis

1. Read the spec document.
2. Read the codebase areas the spec references. Use Grep/Glob/Read to understand what exists and what changes.
3. Build a mental model: what files change, what new files are created, what depends on what.

Do not present anything to the user yet.

### Phase 2: Parallelism Rationale

Present to the user:

- **Work areas identified** — the major logical groupings of changes
- **Parallel work** — which areas can run concurrently and why (different files/subsystems, no shared state between workers)
- **Sequential work** — which areas must be ordered and why (schema before code that uses it, interface before implementation, etc.)
- **Merge risks** — any areas where parallel work might produce merge conflicts when branches are combined

**GATE: User must approve the parallelism structure before proceeding.** Adjust if they disagree. *(Skipped with `--auto`.)*

### Phase 3: Task List

Present the full set of tasks. For each task:

**Title:** Short, descriptive.

**Description** containing:
- **What to do** — the change in 1-2 sentences
- **Scope boundary** — what's in bounds and explicitly what is NOT in bounds (e.g., "add the migration and the Go struct, don't touch the API layer")
- **Expected footprint** — files to create/modify with rough line estimates. This is your sizing basis and helps the worker stay scoped. Not a hard constraint.
- **How to verify** — the test command or acceptance check the worker should run before calling `dt done`
- **Context pointers** — reference spec sections or related tasks rather than duplicating content

**Dependencies:** Which tasks block this one and why.

### Sizing Guidance

A task should be one atomic idea — a single coherent change you can explain in one sentence. The test is not file count or line count but decision count: if a worker needs to make two independent decisions, it's two tasks.

Split when:
- The task requires unrelated changes that don't inform each other.
- A natural description uses "and" to connect two independent actions.
- A reviewer would need to evaluate two separate concerns.

Don't split when:
- Multiple files change but they're all part of the same logical change.
- Tests and implementation are for the same feature.
- The changes only make sense together.

**GATE: User must approve the task list before proceeding.** Adjust if they want changes. *(Skipped with `--auto`.)*

### Phase 4: Review Loop

Dispatch a reviewer subagent (Agent tool, general-purpose) with this prompt:

```
You are reviewing a task decomposition for a dispatch plan.

**Spec:** <spec file path>

**Task list:**
<paste the approved task list>

Check for:
1. Overlapping files between tasks marked as parallel (merge conflict risk)
2. Tasks with scattered scope or too many independent decisions for a single worker
3. Missing dependencies (task B needs task A's output but no dep is wired)
4. Underspecified scope boundaries (description doesn't make clear what's in/out)

Report:
- Approved (if no issues)
- Issues Found: [list each issue with which task and why it matters]
```

If the reviewer finds issues, fix them and re-dispatch. Max 3 iterations, then surface unresolved issues to the user.

### Phase 5: Create

Generate `dt batch` input and execute it. Use back-references (`$1`, `$2`, etc.) to wire parent/child relationships and dependencies atomically.

Descriptions can span multiple lines inside quotes — the batch parser accumulates lines until the closing quote is found.

Format:
```
add "Plan: <spec title>" -d "<plan-level description>"
add "<task 1 title>" -d "<task 1 description>" -p $1
add "<task 2 title>" -d "<task 2 description
that spans multiple lines>" -p $1
add "<task 3 title>" -d "<task 3 description>" -p $1
dep $4 $2
```

`$1` refers to the ID returned by the first `add` (the parent). `$2` is the second `add`, etc.

**Dependency argument order:** `dep A B` means "A depends on B" (B must finish before A can start). If task 4 depends on task 2, write `dep $4 $2`.

**Important:** Avoid literal `$N` patterns in task descriptions — they will be treated as back-references by the batch parser.

Execute:
```bash
echo '<batch input>' | dt batch
```

Verify with:
```bash
dt list --tree
dt show <parent-id>
```

Confirm the task tree looks correct and dependencies are wired as intended.

## What This Skill Does NOT Do

- **Write implementation plans.** No step-by-step instructions, no code snippets. Workers figure out their own approach.
- **Create intermediate documents.** The spec is the source of truth, tasks are the output.
- **Implement anything.** Planning only.
- **Manage chunks or epics.** The parent task is a lightweight grouping mechanism.

## How the Merge Model Works (for context)

You don't need to manage this — the daemon handles it. But understanding it helps you make better decomposition decisions:

- The parent task you create gets a branch (`dispatch/plan-<id>`) when the first child spawns.
- Each child worker gets a worktree branched from the parent branch.
- When a child completes, its branch is merged into the parent branch.
- If the merge conflicts, the task is blocked for human resolution.
- When all children are done, the parent auto-completes and its branch is PR-ready.

This means parallel tasks that touch the same files risk merge conflicts. Wire dependencies between tasks that might conflict, or flag the risk in the parallelism rationale for the user to decide.
