# Phase 3: Worker Prompts, Session Logging, and Review Gates

**Author:** Tal
**Date:** 2026-03-22
**Status:** Draft

---

## 1. What This Adds

Three things that complete Phase 3 of the dispatch PRD:

1. **System prompts** — `worker.md` and `reviewer.md`, loaded by the daemon and injected into Claude Code sessions.
2. **Session logging to disk** — worker and reviewer output written to `~/.dispatch/sessions/<id>.log`.
3. **Review gates** — after a worker exits cleanly, the daemon spawns a reviewer in the same worktree. The reviewer checks the work against the task spec, runs verification, and either approves or rejects with feedback. On rejection, the daemon reopens the task and spawns a fresh worker that can read the accumulated review notes.

Triage (automated crash recovery) is deferred until real crashes inform what recovery patterns are needed.

---

## 2. Review Gate Flow

### Lifecycle

```
Task becomes ready
  → Daemon spawns worker in worktree
  → Worker implements, commits, exits 0
  → Daemon spawns reviewer in the SAME worktree
  → Reviewer reads task spec, reviews code, runs verification
  → Exit 0 (approved): daemon merges branch + marks done
  → Exit non-zero (rejected): daemon reopens task
      → Next poll cycle: fresh worker spawns
      → Worker reads accumulated review feedback from notes
      → Addresses issues, commits, exits 0
      → Reviewer spawns again
      → Repeats until approved or worker calls dt block
```

### No new statuses

The review gate uses existing statuses. The daemon tracks review state internally via a `taskRoles map[string]SpawnRole` that records whether the current process for a task is a worker or reviewer:

- Worker exits cleanly on an active task → spawn reviewer (daemon sets role to reviewer).
- Reviewer exits cleanly → proceed to merge + done.
- Reviewer exits non-zero → check if reviewer added notes. If notes were added since the reviewer spawned, treat as intentional rejection: reopen task, increment review round. If no notes were added, treat as a reviewer crash: block the task with "reviewer crashed" and log tail.

### Worktree lifecycle across review rounds

The worktree and branch are preserved throughout the worker → reviewer → worker cycle. When a reviewer rejects:

1. The worktree stays in place (worker's commits are on the branch).
2. The task is reopened.
3. On next poll, `spawnReady` detects the worktree already exists and skips creation. It spawns a fresh worker in the existing worktree.

This means the new worker sees the previous worker's committed code and can build on it rather than starting from scratch. The worktree is only cleaned up on final completion (after reviewer approves) or when the task is blocked.

`spawnReady` is modified: before creating a worktree, check if one already exists at `~/.dispatch/worktrees/<id>`. If so, skip worktree creation and proceed directly to spawning.

### Daemon restart during review

Review state (`taskRoles`, `reviewRound`) is in-memory and lost on restart. On recovery:

- The daemon sees an active task with a live process but cannot determine if it's a worker or reviewer. It re-adopts as before.
- If the process exits after re-adoption, the daemon treats it as a worker exit (default). This may cause a second review of already-reviewed work, which is harmless.
- `reviewRound` resets to 0. Log file naming recovers by checking for existing log files: on spawn, glob `~/.dispatch/sessions/<id>-review*.log` and set the round counter to match.

### Rejection feedback

The reviewer adds notes to the task before exiting non-zero. Notes are append-only and cumulative — a worker spawned after 3 review rounds sees all previous feedback in `dt show` output. The reviewer structures feedback as:

```
Review round N — REJECTED

Issues:
- <issue 1>
- <issue 2>

What to fix:
- <actionable instruction>
```

### Approval

The reviewer exits 0. No note is required on approval, but the reviewer may add one summarizing what was checked.

### Infinite loop handling

No hard cap on review rounds. Workers are instructed to call `dt block` if they cannot address reviewer feedback or if feedback reveals a fundamental problem with the task's scope or approach. This surfaces the issue to a human rather than looping.

---

## 3. Worker Prompt (`worker.md`)

Injected via `--system-prompt` when spawning worker sessions.

```markdown
You are a dispatch worker. You implement exactly one task.

## Your assignment

Run `dt show $TASK_ID` to read your task description, notes, and dependencies.

If this task has blockers, read their notes — they contain context from previous work
that may be relevant.

## How to work

1. Read the task description carefully. Understand the scope boundary — what is in
   bounds and what is explicitly out of bounds.
2. Implement the change described in the task. Stay within the stated scope.
3. Run the verification command specified in the task description. Do not skip this.
4. Commit your work with a message referencing the task ID.
5. Exit.

## Communication

- Add notes with `dt note $TASK_ID --author worker` to document non-obvious decisions.
- If this task was reopened after a review rejection, previous review feedback is in the
  notes. Read all notes before starting. Address the reviewer's issues.

## What NOT to do

- Do not call `dt done`. The daemon handles task completion after review.
- Do not create new tasks or modify other tasks.
- Do not manage git branches or worktrees.
- Do not work outside the scope boundary defined in your task.

## When to stop

- **Normal completion:** commit your work and exit. A reviewer will check it.
- **Stuck or blocked:** run `dt block $TASK_ID "<what you tried and what decision is needed>"` and exit.
- **Review feedback you can't address:** if the reviewer's feedback reveals a fundamental
  problem with the task (wrong approach, missing dependency, scope too large), call
  `dt block $TASK_ID "<explain why this task needs human attention>"` and exit.
```

`$TASK_ID` is substituted by the daemon before injection.

---

## 4. Reviewer Prompt (`reviewer.md`)

Injected via `--system-prompt` when spawning reviewer sessions.

```markdown
You are a dispatch reviewer. You verify that a worker's implementation meets the task
requirements.

## Your assignment

Run `dt show $TASK_ID` to read the task description and notes.

## What to review

1. **Spec compliance:** Does the implementation match what the task description asked for?
   Check scope boundaries — flag work that goes beyond or falls short of what was specified.
2. **Code quality:** Is the code clear, correct, and maintainable? Look for bugs, edge
   cases, unclear naming, missing error handling.
3. **Verification:** Run the verification command from the task description. It must pass.
4. **Scope creep:** Did the worker make changes outside the task's stated scope? Flag this.

## How to communicate

- If rejecting, add a note with `dt note $TASK_ID --author reviewer` containing:
  - What issues you found
  - What specifically needs to change
  - Structure as: "Review round N — REJECTED" followed by issues and fixes
- Read previous review notes to avoid repeating feedback the worker already addressed.

## Rules

- You MUST NOT modify any code. You are read-only.
- You MUST NOT create, edit, or delete files.
- You MUST NOT create new tasks or modify task state (except adding notes).
- You may read any file in the worktree, run tests, and run verification commands.

## How to finish

- **Approve:** exit with code 0. The daemon will merge and complete the task.
- **Reject:** add your feedback as a note, then exit with a non-zero code.
```

`$TASK_ID` is substituted by the daemon before injection.

---

## 5. Session Logging

Worker and reviewer output is written to disk at `~/.dispatch/sessions/<id>.log`.

### Implementation

The daemon creates a log file when spawning a worker or reviewer. Output is tee'd to both the in-memory ring buffer (for crash block reasons) and the log file. The `RingBuf` is replaced with a `TeeWriter` that writes to both destinations.

```
~/.dispatch/sessions/
  a3f8.log            # worker session for task a3f8 (first run)
  a3f8-review-1.log   # reviewer session, round 1
  a3f8-2.log           # worker session after first rejection
  a3f8-review-2.log   # reviewer session, round 2
```

### File naming

- Worker (first run): `<task_id>.log`
- Worker (after review rejection): `<task_id>-2.log`, `<task_id>-3.log`, etc.
- Reviewer (round 1): `<task_id>-review-1.log`
- Reviewer (round 2): `<task_id>-review-2.log`, etc.

Round numbering starts at 1. The daemon tracks the review round count per task in-memory. On daemon restart, the round count is recovered by globbing existing log files in `~/.dispatch/sessions/`.

### Lifecycle

Log files are NOT cleaned up when tasks complete. They're useful for debugging and for future triage work. A manual `rm -rf ~/.dispatch/sessions/` is sufficient for cleanup. A retention policy can be added later if disk usage becomes a concern.

### Directory creation

The daemon creates `~/.dispatch/sessions/` on startup if it doesn't exist.

---

## 6. Daemon Changes

### Prompt loading

The daemon loads `worker.md` and `reviewer.md` from a configurable path (default: `~/.dispatch/worker.md` and `~/.dispatch/reviewer.md`). New flags:

- `--worker-prompt` / `DISPATCH_WORKER_PROMPT` — path to worker prompt file
- `--reviewer-prompt` / `DISPATCH_REVIEWER_PROMPT` — path to reviewer prompt file

The daemon reads these files on startup and substitutes `$TASK_ID` at spawn time. If the file doesn't exist, the daemon exits with an error — prompts are required, not optional.

### Review tracking

The daemon adds a `reviewRound` map (`map[string]int`) alongside the existing `workers` map. This tracks how many review rounds a task has gone through, used for:

- Log file naming (`<id>-review-2.log`)
- Review round number in the reviewer's context

On task reopen (review rejection), the round counter increments. On task completion or blocking, the entry is removed.

### Modified `monitorWorkers` flow

Current flow (clean exit):
```
worker exits 0 → merge branch → mark done → cleanup
```

New flow (clean exit):
```
worker exits 0 → spawn reviewer in same worktree → reviewer exits 0 → merge branch → mark done → cleanup
                                                  → reviewer exits non-zero → reopen task → next cycle spawns fresh worker
```

The daemon tracks whether the current process for a task is a worker or a reviewer. When a reviewer exits, it follows the appropriate path.

### WorkerSpawner changes

`ClaudeSpawner` gets a second prompt field:

```go
type ClaudeSpawner struct {
    ClaudeBin      string
    WorkerPrompt   string // contents of worker.md (with $TASK_ID placeholder)
    ReviewerPrompt string // contents of reviewer.md (with $TASK_ID placeholder)
    OutputLines    int
}
```

The `WorkerSpawner` interface in `worker.go` adds a `role` parameter:

```go
type SpawnRole string
const (
    RoleWorker   SpawnRole = "worker"
    RoleReviewer SpawnRole = "reviewer"
)

type WorkerSpawner interface {
    Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole) (WorkerHandle, error)
}
```

All implementations must be updated: `ClaudeSpawner`, `MockSpawner` (in `mock_spawner.go`), and all call sites in `daemon.go` (`spawnReady`, and the new reviewer spawn path in `monitorWorkers`).

The reviewer spawn uses the same worktree directory — no new worktree is created.

### Worker crash on reopened tasks

If a worker exits non-zero on a task that has been through review rounds, the standard crash/block flow applies — the task is blocked with log context. No special handling for post-review crashes.

---

## 7. Task Sizing Guidance Update

The dispatch-planner skill's sizing guidance changes from metrics-based heuristics to conceptual atomicity.

**Current (remove):**
> Heuristic: >10 files or >300 lines of non-trivial logic means consider splitting.

**New:**
A task should be one atomic idea — a single coherent change you can explain in one sentence. The test is not file count or line count but decision count: if a worker needs to make two independent decisions, it's two tasks.

Split when:
- The task requires unrelated changes that don't inform each other.
- A natural description uses "and" to connect two independent actions.
- A reviewer would need to evaluate two separate concerns.

Don't split when:
- Multiple files change but they're all part of the same logical change.
- Tests and implementation are for the same feature.
- The changes only make sense together.

---

## 8. What This Does NOT Include

- **Triage flow.** Deferred until real crashes inform recovery patterns. Crashed workers still get blocked with log context.
- **Review round caps.** No hard limit on review iterations. Workers block if stuck.
- **Reviewer code modification.** Reviewers are strictly read-only. If a reviewer could fix a one-line issue, that's still the worker's job.
- **`triage.md` prompt.** Deferred with triage.
