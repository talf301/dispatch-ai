# Phase 5: Multi-Repo Daemon & Automatic PR Creation

**Date:** 2026-03-23
**Status:** Draft

---

## Overview

Two features that complete the dispatch daemon's lifecycle:

1. **Multi-repo support** — A single `dispatchd` process manages tasks across multiple git repositories, configured via `~/.dispatch/config.toml`.
2. **Automatic PR creation** — When a plan's children all complete, the daemon creates a GitHub PR from the plan branch targeting the repo's default branch.

---

## 1. Config File (`~/.dispatch/config.toml`)

### Format

```toml
[[repo]]
path = "/home/user/projects/frontend"
max_workers = 4

[[repo]]
path = "/home/user/projects/backend"
max_workers = 2
```

### Fields

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `path` | string | yes | — | Absolute path to a git repository |
| `max_workers` | int | no | 4 | Max concurrent workers for this repo |

### Notes

- `path` must be an absolute path to a valid git repo (daemon validates on startup).
- Duplicate paths are rejected at parse time.
- Global settings (poll interval, DB path, session dir) remain daemon CLI flags / env vars — they are process-level, not per-repo.
- Future per-repo fields (e.g. `setup_command`, `test_command`) can be added without schema changes.

---

## 2. DB Schema Change

Add a `repo` column to the `tasks` table:

```sql
ALTER TABLE tasks ADD COLUMN repo TEXT;
```

- Nullable for backwards compatibility (existing tasks have no repo).
- Stores the repo path string exactly as it appears in config.toml.
- No foreign key or normalization — just a plain string match.

### Go-side changes

The `Task` struct gains a `Repo *string` field. This touches:
- `AddTask()` signature — gains a `repo` parameter
- All `SELECT` queries in `tasks.go` (`GetTask`, `ListTasks`, `ReadyTasks`, `GetChildren`, etc.) — add `repo` to the column list
- `scanTasks` helper — scan the new column

### CLI Changes

- `dt add` gains a `--repo` flag to set the repo on creation.
- Batch `add` gains a `-r` flag: `add "Task title" -r /path/to/repo`. Added to `batchAdd()`'s manual flag parsing loop.
- `dt edit` gains a `--repo` flag to set/change the repo on an existing task.
- `dt list` and `dt ready` gain an optional `--repo` filter flag.

### Daemon Behavior

- `ReadyTasks()` returns all ready tasks regardless of repo.
- The daemon filters: only spawn tasks whose `repo` matches a configured repo path. Tasks with an unknown or null repo are skipped with a log warning.
- Per-repo `max_workers` is enforced by counting active workers per repo.

---

## 3. `dt init <path>`

Interactive command to build up the config file incrementally.

### Behavior

1. Resolve `<path>` to an absolute path.
2. Verify it is a git repository (check for `.git`).
3. If the repo path already exists in config, print a message and exit.
4. Prompt for `max_workers` (default: 4).
5. If `~/.dispatch/config.toml` doesn't exist, create it.
6. Append a `[[repo]]` block to the file.
7. Print the resulting block so the user sees what was written.

### Example Session

```
$ dt init ~/Documents/my-project
Adding repo: /home/user/Documents/my-project

Max workers [4]: 3

Added to ~/.dispatch/config.toml:

  [[repo]]
  path = "/home/user/Documents/my-project"
  max_workers = 3
```

---

## 4. Daemon Startup

### With config.toml

1. Parse `~/.dispatch/config.toml`.
2. Validate each repo path is an absolute path to a valid git repo.
3. If any repo is invalid, log a warning and skip it (don't fail the whole daemon).
4. Proceed with valid repos.

### Without config.toml (backwards compat)

- If `--repo` flag is set: single-repo mode, behaves exactly as today. Tasks with null repo are spawned against `--repo`.
- If neither config.toml nor `--repo`: print "No repos configured. Run `dt init <path>` to add a repo." and exit.

---

## 5. Automatic PR Creation

### Trigger

Parent auto-completion currently happens inside `db.DoneTask()` — when the last child completes, the parent silently transitions to done. The daemon has no hook for this.

**Change:** `DoneTask()` returns an `*AutoComplete` struct (nil if no parent auto-completed):

```go
type AutoComplete struct {
    ParentID string
    Repo     *string
}
```

The daemon checks the return value in `handleReviewApproval`. If non-nil, it triggers PR creation for the parent.

### PR Creation Flow

1. Push the plan branch: `git push origin dispatch/plan-<parent-id>`
2. Fetch the parent task (for title) and all notes on it.
3. Format notes into the PR body.
4. Run `gh pr create` from the repo directory.

### PR Body Assembly

Workers add a completion note to their parent task before calling `dt done`:

```
dt note <parent-id> "Implemented X: added foo.go, updated bar.go, added tests"
```

Workers discover their parent ID via `$PARENT_ID`, a new prompt variable injected by the spawner alongside `$TASK_ID`. Workers without a parent (standalone tasks) get `$PARENT_ID` set to empty string and skip the note step.

At PR time, the daemon:

1. Fetches the parent task (for title).
2. Fetches all notes on the parent task.
3. Formats notes into the PR body: each note becomes a bullet under a summary header.

### PR Format

```
Title: <parent task title>

Body:
## Summary

- <worker 1 note>
- <worker 2 note>
- ...

---
Created by [dispatch](https://github.com/dispatch-ai/dispatch)
```

### Target Branch

The PR targets the repo's default branch (detected via `DetectDefaultBranch`, same as existing logic).

### `gh` CLI Invocation

```bash
# Run from the repo directory (cmd.Dir = repoPath)
git push origin dispatch/plan-<parent-id>
gh pr create \
  --head dispatch/plan-<parent-id> \
  --base <default-branch> \
  --title "<parent task title>" \
  --body "<assembled body>"
```

The daemon shells out to `gh` with `cmd.Dir` set to the repo path — no `--repo` flag needed. `gh` must be installed and authenticated.

### Failure Handling

If `git push` or `gh pr create` fails for any reason (not installed, not authenticated, network error, branch already has a PR):

1. The parent task is blocked with the error message as `block_reason`.
2. The plan branch is preserved locally.
3. The user can create the PR manually, or fix the issue and run `dt reopen <parent-id>`.

**Re-trigger mechanism:** The daemon's main loop checks for parent tasks that are blocked with a PR-related block reason and whose plan branch still exists. When the user reopens such a parent, the daemon detects it is a completed plan (all children done, has a plan branch) and retries PR creation. This is a separate check from `ReadyTasks()`, which correctly excludes parent tasks.

### Worker Prompt Change

Add to `worker.md`:

> If you have a parent task, summarize what you changed before completing:
> `dt note $PARENT_ID "<brief summary of your changes>"`
> Include which files you created or modified and what the change accomplishes.
> Then call `dt done $TASK_ID`.

---

## 6. Daemon Structural Changes

### Daemon struct

Replace the single `repoPath string` field with:

```go
type RepoConfig struct {
    Path       string
    MaxWorkers int
}

// On the Daemon struct:
repos      map[string]RepoConfig  // keyed by repo path
workerRepo map[string]string      // taskID → repo path (for post-spawn lookups)
```

In single-repo backwards-compat mode, `repos` contains one entry built from `--repo`.

All methods that currently use `d.repoPath` (`handleReviewApproval`, `handleWorkerCrash`, `handleReviewerExit`, `cleanOrphanedWorktrees`, `recoverActive`) look up the repo via `d.workerRepo[taskID]` or the task's `Repo` field from the DB.

### spawnReady() modifications

Current flow:
1. Call `db.ReadyTasks()`
2. For each task, up to `MaxWorkers`: claim, create worktree, spawn

New flow:
1. Call `db.ReadyTasks()`
2. Group tasks by `repo` field
3. For each repo:
   - Look up repo config (path, max_workers)
   - Count active workers for this repo
   - For each task, up to per-repo `max_workers` minus active count: claim, create worktree in correct repo, spawn
   - Record `d.workerRepo[taskID] = repoPath`

### Worktree creation

`CreateWorktree` already takes `repoDir` as a parameter. The daemon passes the correct repo path from the task's config.

### recoverActive and cleanOrphanedWorktrees

- `recoverActive`: loads each active task from DB, reads `task.Repo` to determine which repo it belongs to, populates `d.workerRepo`.
- `cleanOrphanedWorktrees`: iterates worktree directory, looks up task in DB to find repo, runs git cleanup against the correct repo.

### Worker context

Workers need to know which repo they're in. The worktree directory is already set as the working directory for the spawned process, so this works automatically.

### LogSuffix thread safety

Currently `ClaudeSpawner.LogSuffix` is mutated on the shared struct before each `Spawn()` call — a latent race condition exacerbated by multi-repo. Fix: pass `logSuffix` as a parameter to `Spawn()` instead. Update the `WorkerSpawner` interface:

```go
type WorkerSpawner interface {
    Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix string) (WorkerHandle, error)
}
```

---

## Deferred

- **Non-default base branches for PRs** — Currently PRs always target the repo's default branch. A future enhancement could allow plan-level tasks to specify a base branch (e.g. for project branches off a feature branch), enabling nested PR workflows. This would also enable standalone (non-plan) tasks to produce PRs from non-main base branches.
- **Per-repo setup commands** — `setup_command` field in config.toml, run in worktree after creation (e.g. `npm install`). Straightforward to add once multi-repo is in place.
- **Per-repo test commands** — `test_command` field, used by workers/reviewers for verification.
- **Config hot-reload** — Currently config is loaded once at startup. Could watch the file for changes.
- **Retry limits** — Phase 4 concern, orthogonal to multi-repo.

---

## Exit Criteria

1. Create `~/.dispatch/config.toml` with two repos via `dt init`. Start daemon. Tasks for each repo spawn in the correct repo's worktrees.
2. Per-repo `max_workers` is respected — repo A with `max_workers=2` never has more than 2 concurrent workers even if repo B has capacity.
3. Plan with 3 child tasks completes. Daemon pushes plan branch and creates a PR with notes from all workers in the body.
4. `gh` not installed — daemon blocks the parent with a clear error, branch preserved.
5. Daemon started without config.toml but with `--repo` flag works exactly as before (backwards compat).
6. Daemon started with neither config nor `--repo` prints helpful message and exits.
7. PR creation failure — parent blocked, user reopens, daemon retries PR creation successfully.
