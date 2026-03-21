# Dispatch Planner Design

**Author:** Tal
**Date:** 2026-03-21
**Status:** Draft

---

## 1. What This Is

A skill (`dispatch-planner`) that decomposes an approved spec into a set of `dt` tasks with dependency wiring, sized for parallel execution by dispatch workers. It replaces `writing-plans` in the superpowers flow when the target execution environment is dispatch.

The planner is the bridge between "we know what to build" (the spec) and "workers are building it" (dispatch tasks). It reasons about what can be parallelized, how to scope work so individual workers stay in context, and how the completed work comes back together.

---

## 2. Invocation

**Skill name:** `dispatch-planner`
**Type:** Rigid (follow exactly)
**Position in flow:** After brainstorming produces an approved spec, in place of `writing-plans`.

**Preconditions:**
- A spec document exists (the skill asks for its path if not obvious from conversation context)
- The `dt` binary is on PATH

**Announce at start:** "Using dispatch-planner to decompose the spec into parallel tasks."

---

## 3. Process Flow

### Phase 1: Analysis

Read the spec. Read the relevant codebase areas the spec references. Build a model of what changes where.

### Phase 2: Parallelism Rationale

Present a short summary to the user:

- The major work areas identified
- Which can run concurrently and why (different files/subsystems, no shared state)
- Which must be sequential and why (schema changes before code that uses them, interface definitions before implementations)
- Any tasks that are risky to parallelize (touching adjacent code, potential merge conflicts)

**Gate: user approves or adjusts the parallelism structure before proceeding.**

### Phase 3: Task List

Present the full set of tasks — title, description, and dependency wiring. See section 4 for task description format and section 5 for sizing guidelines.

**Gate: user approves or adjusts the task list before proceeding.**

### Phase 4: Review Loop

Dispatch a reviewer subagent that checks:

- Overlapping files between tasks marked as parallel
- Tasks with too many independent decisions or scattered scope (see sizing guidelines)
- Missing dependencies (task B clearly needs task A's output but no dep wired)
- Underspecified scope boundaries (description doesn't make clear what's in/out)

Fix issues and re-dispatch. Max 3 iterations, then surface to human for guidance.

### Phase 5: Create

Run `dt batch` to create the parent task and all child tasks atomically. Wire dependencies with `dt dep`.

---

## 4. Task Description Format

Each task description gives the worker enough context to execute without being a full implementation plan. The worker figures out *how* — the description specifies *what* and *where*.

A task description includes:

- **What to do** — the change in a sentence or two
- **Scope boundary** — what's in bounds, and explicitly what's *not* (e.g., "add the migration and the Go struct, don't touch the API layer")
- **Expected footprint** — files to create/modify with rough line estimates. This is the planner's sizing basis and helps the worker stay scoped. Not a hard constraint — the worker can deviate if needed.
- **How to verify** — the test command or acceptance check the worker should run before calling `dt done`
- **Context pointers** — references to spec sections or related tasks ("see spec section 3 for the schema" rather than duplicating it)

---

## 5. Task Sizing

Tasks are sized by **scope coherence** and **decision density**, not hard line counts.

**Scope coherence:** The task touches one logical area. A task that modifies the database layer is coherent. A task that modifies the database layer, the API endpoints, and the frontend components is not — even if the total line count is small.

**Decision density:** How many non-obvious choices does the worker need to make? Boilerplate and tests are cheap (low decision density). Novel logic, API design, and error handling are expensive (high decision density).

**Heuristic:** If estimating >10 files or >300 lines of non-trivial logic, consider splitting. Boilerplate and test code don't count against this — a task that generates 400 lines of tests alongside 50 lines of implementation is fine.

**The test:** Can a worker hold the full context of this task and execute it reliably without going off the rails?

---

## 6. Merge Model

### Parent Branch

The planner creates a parent task representing the overall plan. This parent task gets a long-lived branch (`dispatch/plan-<id>`). The parent task is never assigned to a worker — it exists as a grouping mechanism and merge target.

All child tasks are created with `-p <parent_id>`.

### Worker Branching

When the daemon spawns a worker for a child task, the worktree branches from the parent branch (not from main). This means workers see all previously completed and merged work from sibling tasks.

### Merge on Completion

When a child task completes successfully:

1. Daemon merges the child's branch into the parent branch
2. If merge is clean: delete the child branch and worktree
3. If merge conflicts: block the task, surface to human for resolution

Dependent tasks don't start until their blockers have completed *and been merged into the parent branch*. This guarantees a worker always sees the work it depends on.

### Plan Completion

When all child tasks are done, the parent branch contains all completed work. The parent task auto-completes. The parent branch is the PR branch.

### Daemon Changes Required

These changes are part of Phase 3 scope:

- Tasks with children are never spawned as workers
- Child worktrees branch from parent's branch, not the base branch
- On child completion: merge into parent branch, then clean up child branch and worktree
- On merge conflict: block the task
- Parent task auto-completes when all children are done
- Stop deleting branches on clean exit for child tasks (merge into parent instead)

---

## 7. What the Planner Does NOT Do

- **Write implementation plans.** No step-by-step instructions, no code snippets. Workers figure out their own approach.
- **Create intermediate documents.** The spec is the source of truth, tasks are the output. No plan document between them.
- **Implement anything.** Planning only.
- **Manage chunks/epics.** The parent task is a lightweight grouping mechanism, not a full epic system.

---

## 8. Relationship to PRD Phases

The planner is added to **Phase 3** alongside the other prompt files (worker.md, triage.md). Phase 3 scope becomes:

- `worker.md` system prompt — injected into worker sessions
- `triage.md` system prompt — injected into triage sessions on worker crash
- `dispatch-planner` skill — decomposes specs into task graphs
- Session logging to disk (`~/.dispatch/sessions/<id>.log`)
- Parent task / merge model — daemon support for plan branches
- Triage flow — spawn triage agent on worker crash

### Phase 3 Exit Criteria

Original criteria plus:
- Planner decomposes a spec into ≥5 tasks with dependencies
- Parent branch accumulates completed child work via merge
- Dependent tasks see blocker's merged work when they start
- Merge conflict on completion blocks the task
- Parent auto-completes when all children done
- Parent branch is PR-ready

---

## 9. PRD Updates Required

The following PRD sections need updating to reflect this design:

1. **Section 2.3 (Prompt files):** Expand `planner.md` description to reference this spec. Note that the planner is delivered as a skill, not just a prompt file.
2. **Phase 3 scope (Section 5):** Add planner skill, parent task concept, and merge model to Phase 3 deliverables. Update exit criteria.
3. **Worktree cleanup (Section 2.2):** Update to reflect that child task branches merge into parent rather than being deleted on clean exit.
4. **Phase 2 implementation notes (Section 8):** Note that branch deletion on clean exit will change in Phase 3 when parent tasks are introduced.
