# Phase 2: `dispatchd` — Spawn + Monitor Daemon

**Date:** 2026-03-20
**Status:** Draft

---

## Overview

A long-running daemon that polls the dispatch SQLite database for ready tasks, spawns Claude Code workers in git worktrees, monitors their lifecycle, and updates task state on completion or failure. No triage agent — crashed workers get blocked with log context for human review.

---

## Architecture

### Main Loop

Runs every 5 seconds (configurable via `--poll-interval`):

1. Call `db.ReadyTasks()` for available work.
2. For each ready task, up to `max_workers` concurrently:
   a. `db.ClaimTask(id, sessionID)` — claim first to prevent double-spawn from concurrent poll cycles. If claim fails (already claimed), skip.
   b. Create a git worktree: `git worktree add <worktree_dir> -b dispatch/<id> <base_branch>`
   c. Spawn a worker process (via `WorkerSpawner` interface) in the worktree.
   d. Write PID file to `<worktree_dir>/worker.pid`.
   e. If worktree creation or spawn fails: `db.ReleaseTask(id)`, clean up, log error.
3. For each active worker:
   a. Check if the process is still alive.
   b. If exited cleanly (code 0): check if task is already done (worker called `dt done` itself). If not, call `db.DoneTask(id)`. Tear down worktree and delete branch.
   c. If exited non-zero: capture last 100 lines of output, `db.BlockTask(id, reason)` with exit code and log tail. Remove worktree but keep the branch for human inspection.
4. Clean up orphaned worktrees — scan `~/.dispatch/worktrees/`, remove any that don't correspond to an active task.
5. Log state summary to stderr.

### WorkerSpawner Interface

Worker spawning is behind an interface to enable testing without Claude and to allow alternative worker implementations later.

```go
type WorkerSpawner interface {
    // Spawn starts a worker for the given task in the given directory.
    // Returns a WorkerHandle for monitoring the process.
    Spawn(ctx context.Context, task db.Task, workDir string) (WorkerHandle, error)
}

type WorkerHandle interface {
    // PID returns the OS process ID.
    PID() int
    // Wait blocks until the process exits and returns the exit error (nil for code 0).
    Wait() error
    // Output returns the captured stdout/stderr tail (last 100 lines).
    Output() string
}
```

**Default implementation (`ClaudeSpawner`):** shells out to the `claude` CLI. The system prompt comes from `worker.md` (containing full worker instructions per PRD section 2.3). The initial user message is the task reference: `"Your task ID is <id>. Run dt show <id> to read your assignment."`

Exact CLI flags depend on what Claude Code supports — the spawner encapsulates this so changes are localized.

Stdout and stderr are combined and captured in a ring buffer (last 100 lines) for crash context. No disk logging in Phase 2 — deferred to Phase 3 when the triage agent needs full session logs.

### Git Worktree Management

**Create:** `git worktree add ~/.dispatch/worktrees/<task_id> -b dispatch/<task_id> <base_branch>`

- `base_branch` defaults to the repo's default branch (detected via `git symbolic-ref refs/remotes/origin/HEAD` or fallback to `main`).
- Configurable via `--base-branch` flag.

**Teardown on clean exit:** `git worktree remove ~/.dispatch/worktrees/<task_id>` followed by `git branch -D dispatch/<task_id>`. Both worktree and branch are removed.

**Teardown on unclean exit:** `git worktree remove ~/.dispatch/worktrees/<task_id>`. The branch (`dispatch/<task_id>`) is kept so humans can inspect partial work.

All git operations (worktree create/remove, branch operations, base branch detection) are executed relative to the `--repo` path.

### PID Files

Written to `~/.dispatch/worktrees/<task_id>/worker.pid` immediately after spawning. Contains the PID as a decimal string.

Used for daemon restart recovery (see below).

### Startup Recovery

On daemon start:

1. Query `db.ListTasks("active", false)` to get all active tasks.
2. For each active task:
   a. Check if worktree exists at `~/.dispatch/worktrees/<task_id>`.
   b. If worktree exists, read `worker.pid` and check if the process is alive (signal 0).
   c. **Live process:** re-adopt into the in-memory PID→task map, resume monitoring.
   d. **Dead process:** `db.BlockTask(id, "worker died while daemon was down")`.
   e. **No worktree or no PID file:** `db.BlockTask(id, "unknown worker state after daemon restart")`.
3. Clean up orphaned worktrees that don't correspond to any active task.

### Graceful Shutdown

On SIGINT or SIGTERM:

1. Stop polling for new tasks.
2. Send SIGTERM to all active worker processes.
3. Wait up to 30 seconds for workers to exit.
4. If workers haven't exited, log a warning and exit anyway (workers become orphans, handled on next daemon start via recovery).

Workers that are mid-`dt done` when SIGTERM arrives may leave tasks in an intermediate state. The startup recovery logic handles this on next daemon start.

---

## Configuration

CLI flags with environment variable fallbacks. No config file (deferred to Phase 5 with multi-repo support).

| Flag | Env Var | Default | Description |
|------|---------|---------|-------------|
| `--db` | `DISPATCH_DB` | `~/.dispatch/dispatch.db` | Database path |
| `--max-workers` | `DISPATCH_MAX_WORKERS` | `4` | Max concurrent workers |
| `--base-branch` | `DISPATCH_BASE_BRANCH` | auto-detect | Branch to create worktrees from |
| `--repo` | `DISPATCH_REPO` | `.` (cwd) | Git repository for all worktree/branch operations |
| `--poll-interval` | `DISPATCH_POLL_INTERVAL` | `5s` | Main loop poll interval |

---

## Process State

PID→task mapping is held **in memory only**. The database does not store PIDs — this is ephemeral orchestration state. PID files in worktrees bridge daemon restarts.

---

## Binary

`cmd/dispatchd/main.go` — single binary, built alongside `dt`.

Uses:
- `internal/db` — direct import, same package the CLI uses
- `os/exec` — process spawning
- `os/signal` — graceful shutdown
- `cobra` — CLI parsing (consistent with `dt`)

No new external dependencies.

---

## Error Handling

- **Claim fails:** another daemon or poll cycle got it first. Skip, no error.
- **Worktree creation fails:** release the claim, log error, skip task (will be retried next poll).
- **Worker spawn fails:** remove worktree, release the claim, log error.
- **DB write fails:** log error. Task state may be stale — next poll will re-evaluate.
- **Multiple failures on same task:** no retry limit in Phase 2. A task that keeps failing will keep being picked up. (Can add retry limits in Phase 4.)

---

## What's Deferred

- **Session logging to disk** — Phase 3, needed for triage agent context.
- **Config file (`config.toml`)** — Phase 5, needed for multi-repo routing.
- **Triage agent** — Phase 3. Crashed workers just get blocked with log tail.
- **Setup commands** (e.g., `npm install` in worktree) — Phase 5, per-repo config.
- **Retry limits** — Phase 4, based on real usage.

---

## Exit Criteria

From the PRD:

1. Create a task manually. Start daemon. Worker spawns in a worktree, implements the task, calls `dt done`, daemon tears down worktree.
2. Kill a worker with SIGKILL. Daemon detects death, blocks the task with log tail.
3. Kill the daemon. Restart it. Active tasks with dead workers get blocked. Active tasks with live workers get re-adopted.
4. Daemon respects `--max-workers` limit.
