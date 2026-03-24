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

### CLI Changes

- `dt add` gains a `--repo` flag to set the repo on creation.
- Batch `add` gains a `-r` flag: `add "Task title" -r /path/to/repo`.
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

When the last child task of a parent completes (parent auto-completes via `DoneTask()`), the daemon creates a PR.

This happens in the existing `handleReviewApproval` flow — after the final child's branch is merged into `dispatch/plan-<parent-id>` and the parent transitions to done.

### PR Body Assembly

Workers add a completion note to their parent task before calling `dt done`:

```
dt note <parent-id> "Implemented X: added foo.go, updated bar.go, added tests"
```

At PR time, the daemon:

1. Fetches the parent task (for title).
2. Fetches all notes on the parent task.
3. Formats notes into the PR body: each note becomes a bullet under a summary header.
4. Runs `gh pr create`.

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

The PR targets the repo's default branch (detected via `git symbolic-ref refs/remotes/origin/HEAD` or fallback, same as existing `DetectDefaultBranch`).

### `gh` CLI Invocation

```bash
gh pr create \
  --repo <repo-path> \
  --head dispatch/plan-<parent-id> \
  --base <default-branch> \
  --title "<parent task title>" \
  --body "<assembled body>"
```

The daemon shells out to `gh` — no GitHub API client. `gh` must be installed and authenticated.

### Failure Handling

If `gh pr create` fails for any reason (not installed, not authenticated, network error, branch already has a PR):

1. The parent task is blocked with the error message as `block_reason`.
2. The plan branch is preserved.
3. The user can fix the issue and reopen the parent (which re-triggers PR creation), or create the PR manually.

### Worker Prompt Change

Add to `worker.md`:

> Before calling `dt done`, summarize what you changed by running:
> `dt note <parent-id> "<brief summary of your changes>"`
> Include which files you created or modified and what the change accomplishes.

Workers without a parent task skip this step.

---

## 6. Daemon Routing Changes

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

### Worktree creation

`CreateWorktree` already takes `repoDir` as a parameter. The daemon just passes the correct repo path from the task's config instead of the single `--repo` value.

### Worker context

Workers need to know which repo they're in. The worktree directory is already set as the working directory for the spawned process, so this works automatically — `dt` commands run relative to the worktree, and `git` operations happen in the right repo.

---

## Deferred

- **Non-default base branches for PRs** — Currently PRs always target the repo's default branch. A future enhancement could allow plan-level tasks to specify a base branch (e.g. for project branches off a feature branch), enabling nested PR workflows.
- **Per-repo setup commands** — `setup_command` field in config.toml, run in worktree after creation (e.g. `npm install`). Straightforward to add once multi-repo is in place.
- **Per-repo test commands** — `test_command` field, used by workers/reviewers for verification.
- **Config hot-reload** — Currently config is loaded once at startup. Could watch the file for changes.
- **Retry limits** — Phase 4 concern, orthogonal to multi-repo.

---

## Exit Criteria

1. Create `~/.dispatch/config.toml` with two repos via `dt init`. Start daemon. Tasks for each repo spawn in the correct repo's worktrees.
2. Per-repo `max_workers` is respected — repo A with `max_workers=2` never has more than 2 concurrent workers even if repo B has capacity.
3. Plan with 3 child tasks completes. Daemon creates a PR on the plan branch with notes from all workers in the body.
4. `gh` not installed — daemon blocks the parent with a clear error, branch preserved.
5. Daemon started without config.toml but with `--repo` flag works exactly as before (backwards compat).
6. Daemon started with neither config nor `--repo` prints helpful message and exits.
