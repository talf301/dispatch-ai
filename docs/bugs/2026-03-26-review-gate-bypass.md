# Bug: Review Gate Bypass — Rejected Tasks Marked Done

**Date:** 2026-03-26
**Severity:** Critical
**Component:** dispatchd review gate (`daemon.go`: `handleWorkerComplete`, `handleReviewerExit`)
**Reproduction:** 100% — occurred on 4 of 7 tasks in graph-pilot plan 72c3

---

## Summary

Workers commit code to `main` instead of their worktree branch. The reviewer correctly identifies empty branches and rejects with detailed notes, but the task is marked `done` immediately after rejection. The review gate is entirely bypassed.

## Observed Behavior

Plan 72c3 (`gp serve`) had 7 child tasks. All 7 workers ran, all 7 reviewers ran. Results:

| Task | Reviewer Verdict | Actual Status | Code Location |
|------|-----------------|---------------|---------------|
| 474b (tmux.ts) | **REJECTED** — "No tmux.ts file was created" | done | Committed to main |
| 4719 (index.html) | **REJECTED** — "zero new commits" | done | Committed to main |
| ebcf (serve.ts) | Approved | done | Committed to main |
| 8db3 (graph.js) | Approved | done | On dispatch/plan-72c3 branch |
| a8ca (actions.js) | **REJECTED** — "zero commits, file does not exist" | done | Committed to main |
| fd11 (filters.js) | **REJECTED** — "zero commits on dispatch/fd11" | done | Committed to main |
| e408 (cli.ts) | Approved | done | Committed to main |

Evidence from task notes (all 4 rejected tasks follow this pattern):

```
[22:53:53] reviewer: Review round 1 — REJECTED
  ...detailed feedback...
[22:54:02] system: Status changed: active → done
```

The rejection note and the `done` transition are seconds apart. No second worker run occurred. No `-2.log` session files exist.

## Root Cause Analysis

Two bugs are interacting:

### Bug 1: Workers commit to main, not the worktree branch

Workers are committing their code directly to `main` instead of the worktree branch (`dispatch/<task-id>`). Evidence:

```
$ git log main --oneline -- graphpilot/src/tmux.ts
e55946d feat(474b): add tmux session manager for gp serve actions

$ git ls-tree dispatch/plan-72c3 -- graphpilot/src/tmux.ts
(empty — file does not exist on the plan branch)
```

All 7 workers produced code, but 6 of 7 committed to `main`. Only task 8db3 (graph.js) ended up on the plan branch. The `git log main` shows commits from all workers landing directly on main:

```
1b7c7a0 Add gp serve command to CLI for web dashboard        (e408)
d4e5636 Add Express server with REST API, WebSocket...        (ebcf)
9533fd6 feat(a8ca): add action handlers and detail panel      (a8ca)
15fe1c4 Add filter controls for graph dashboard sidebar       (fd11)
e813e19 feat(4719): add dashboard HTML/CSS                    (4719)
e55946d feat(474b): add tmux session manager                  (474b)
```

**Possible causes:**
- Worktree setup failed silently and workers ran in the main repo
- Worktree was created but `claude --print` inherited the parent's cwd instead of the worktree path
- Worker's git config in the worktree defaulted to pushing/committing to main
- Multiple workers sharing the same repo caused branch confusion

### Bug 2: Rejected tasks transition to done despite reviewer exit non-zero

Per the Phase 3 spec, `handleReviewerExit` should:
1. Check if new notes were added since reviewer started
2. If yes (intentional rejection): reopen the task
3. If no (crash): block the task

In practice, the reviewer adds notes and exits non-zero, but the task goes to `done` within seconds. Possible code paths that could cause this:

**Hypothesis A: Worker called `dt done` before exiting**

`daemon.go` line 393 has a guard:
```go
if task.Status != "done" {
    // validate branch has commits
}
```

If the worker calls `dt done` directly (which worker.md says not to do, but doesn't prevent), the task transitions to `done` in the DB before `handleWorkerComplete` runs. When the daemon checks, it sees `Status == "done"` and skips branch validation entirely. The reviewer then runs, rejects, but the task is already `done` in the DB.

The question is: does `handleReviewerExit` with non-zero exit actually reopen a task that's already `done`? Or does it only handle `active` tasks?

**Hypothesis B: Race between worker note (via `dt note`) and status check**

The daemon records `noteCountAtReviewStart` before spawning the reviewer. If the timing is wrong (e.g., the reviewer's rejection note hasn't been committed to the DB by the time `handleReviewerExit` checks), the daemon sees no new notes and treats it as a crash — but then the block path might also fail.

**Hypothesis C: Process exit handler fires before reviewer note is written**

If the reviewer process exits (non-zero) before the `dt note` command completes (since `dt note` is a subprocess call from within the Claude session), the daemon's exit handler might fire first, see no new notes, and take the crash path. But the crash path should block, not mark done.

## Session Logs

All session logs are at `~/.dispatch/sessions/`:

```
474b.log            # worker session
474b-review-1.log   # reviewer — REJECTED (no second worker run)
4719.log
4719-review-1.log   # reviewer — REJECTED
a8ca.log
a8ca-review-1.log   # reviewer — REJECTED
fd11.log
fd11-review-1.log   # reviewer — REJECTED
ebcf.log
ebcf-review-1.log   # reviewer — approved
8db3.log
8db3-review-1.log   # reviewer — approved
e408.log
e408-review-1.log   # reviewer — approved
```

No `-2.log` files exist for any task, confirming no second worker was ever spawned after rejection.

## Downstream Impact

Because review gates didn't hold:
- Code landed on `main` unreviewed, with integration bugs across modules
- The plan branch (`dispatch/plan-72c3`) only received 1 of 7 merges
- The daemon attempted `gh pr create` on the incomplete plan branch, which failed (no GitHub auth), and blocked the parent task

Integration bugs found in the unreviewed code:
1. Edge field name mismatch between server (`type`) and client (`edgeType`)
2. Wrong edge type string (`'dependency'` vs `'depends-on'`)
3. Infinite daemonize fork loop (no `--foreground` flag for child)
4. `import.meta.dirname` undefined on Node 18
5. `fs.watch({ recursive: true })` unsupported on Linux
6. `stopServer()` didn't read PID file to kill daemon
7. Design button double-bound in two modules
8. Layout toggle double-bound in two modules
9. Default port mismatch (3742 vs 4800)

## Reproduction

1. Create a plan with 3+ child tasks via `dt batch`
2. Wait for workers to run
3. Observe: do workers commit to worktree branches or main?
4. Observe: when reviewers reject, does the task reopen or go to done?

## Investigation Checklist

- [ ] Check `handleWorkerComplete`: does the `task.Status != "done"` guard allow workers who call `dt done` to bypass review?
- [ ] Check `handleReviewerExit`: does it actually call reopen when the task is already `done`?
- [ ] Check worktree creation: are worktrees being created correctly? Is the worker's cwd set to the worktree?
- [ ] Check `claude_spawner.go`: is the `Dir` field on the spawned process set to the worktree path?
- [ ] Check worker.md: does it tell the worker to call `dt done`? (It shouldn't, but if it does, that's Bug 1's cause)
- [ ] Add integration test: spawn a worker that makes no commits, verify task does NOT reach `done`
- [ ] Add integration test: spawn a worker that calls `dt done` directly, verify review still runs and can reject

## Suggested Fixes

### For Bug 1 (workers on wrong branch):
- Verify worktree creation succeeds before spawning worker
- Set worker process cwd explicitly to worktree path
- Consider adding a pre-commit hook in worktrees that rejects commits to main

### For Bug 2 (rejection bypass):
- Remove the `task.Status != "done"` guard in `handleWorkerComplete` — always validate the branch
- Or: prevent `dt done` from being callable by workers (check if task has a parent plan and assignee matches a worker pattern)
- In `handleReviewerExit`: if task is already `done`, force-reopen it when reviewer rejects
- Add a log line when a task transitions to `done` to trace which code path caused it
