# PRD: Dispatch (Minimal)

**Author:** Tal  
**Date:** 2026-03-20  
**Status:** Draft v1

---

## 1. What This Is

A minimal task tracker and orchestration daemon for coordinating multiple Claude Code agents on a single developer's machine. Two binaries, one SQLite database, a few prompt files.

The system has two jobs:
1. **Track tasks** — create, decompose, depend, claim, close, block.
2. **Run workers** — pick ready tasks, spawn Claude Code in git worktrees, detect completion or failure.

Everything else is deferred until real usage proves it's needed.

---

## 2. Components

### 2.1 `dt` — CLI task tracker

A single binary that reads and writes a SQLite database. All state lives in the database. The CLI is the only interface — agents and humans use the same commands.

**Database location:** `~/.dispatch/dispatch.db` (override with `--db <path>` or `DISPATCH_DB`).

SQLite pragmas: WAL mode, `busy_timeout = 5000`, `foreign_keys = ON`.

#### Schema

Three tables:

```sql
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,   -- short random ID, e.g. "a3f8"
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','active','blocked','done')),
    block_reason TEXT,              -- set when status = 'blocked'
    assignee    TEXT,               -- session ID of claiming agent
    parent_id   TEXT REFERENCES tasks(id),
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE deps (
    blocker_id  TEXT NOT NULL REFERENCES tasks(id),
    blocked_id  TEXT NOT NULL REFERENCES tasks(id),
    PRIMARY KEY (blocker_id, blocked_id)
);

CREATE TABLE notes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id     TEXT NOT NULL REFERENCES tasks(id),
    content     TEXT NOT NULL,
    author      TEXT,               -- 'human', session ID, or 'triage'
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

#### ID generation

4-character lowercase hex, randomly generated. Check for collision on insert (vanishingly unlikely at <10k tasks but worth the one-line check). Short IDs keep CLI usage ergonomic — `dt show a3f8` not `dt show bd-a3f8c7e2`.

#### Commands

```
dt add <title> [-d <description>] [-p <parent>] [--after <blocker>]
    Create a task. Prints the new ID.

dt edit <id> [-t <title>] [-d <description>]
    Update title or description.

dt dep <blocker> <blocked>
    Add a dependency. Errors on cycles.

dt undep <blocker> <blocked>
    Remove a dependency.

dt claim <id> <assignee>
    Set status=active, assignee=<assignee>. Fails if already claimed.

dt release <id>
    Set status=open, clear assignee.

dt done <id>
    Set status=done. Clears assignee.

dt block <id> <reason>
    Set status=blocked, block_reason=<reason>. Clears assignee.

dt reopen <id>
    Set status=open. Clears block_reason and assignee.

dt note <id> <content>
    Append a note. Reads from stdin if content is omitted.

dt ready
    List unclaimed, unblocked, open tasks. Ordered by:
      1. Number of tasks this would unblock (descending)
      2. Created date (oldest first)

dt list [--tree] [--all] [--status <s>]
    List tasks. --tree shows parent/child hierarchy.
    Hides done tasks by default. --all includes them.

dt show <id>
    Print task details, notes, dependencies, and status history.

dt batch
    Read commands from stdin, execute in a single transaction.
    One command per line, same syntax as above.
```

All commands support `--json` for machine-readable output.

#### Cycle detection

On `dt dep`, walk the dependency graph from `blocked` to check if `blocker` is reachable. Reject with error if so. Simple DFS, fine at the scale we're operating at.

#### `dt ready` implementation

```sql
SELECT t.* FROM tasks t
WHERE t.status = 'open'
  AND t.assignee IS NULL
  AND NOT EXISTS (
    SELECT 1 FROM deps d
    JOIN tasks blocker ON d.blocker_id = blocker.id
    WHERE d.blocked_id = t.id
    AND blocker.status != 'done'
  )
ORDER BY (
    SELECT COUNT(*) FROM deps d2
    WHERE d2.blocker_id = t.id
    AND EXISTS (SELECT 1 FROM tasks t2 WHERE t2.id = d2.blocked_id AND t2.status != 'done')
  ) DESC,
  t.created_at ASC;
```

---

### 2.2 `dispatchd` — Orchestration daemon

A long-running process. No socket protocol, no IPC. It reads and writes the same SQLite database as the CLI. Communicates with the outside world through the database and the filesystem.

#### Database access

The daemon imports `internal/db` directly rather than shelling out to `dt`. This gives it compiled Go function calls (`db.ReadyTasks()`, `db.ClaimTask()`, etc.) instead of process spawning, JSON parsing, and exit code handling on every state transition. The `db.Open(path)` function takes a path, so the daemon can manage multiple databases (one per project) by holding multiple `*DB` instances — this does not couple it to a single database.

The `dt` CLI remains the interface for humans and for Claude Code agent sessions (workers, triage). The daemon uses the library.

#### Main loop (every 5 seconds)

```
1. Call db.ReadyTasks() for available work.
2. For each ready task, up to the concurrency limit:
   a. Create a git worktree: git worktree add ~/.dispatch/worktrees/<id> -b dispatch/<id> <base_branch>
      (base_branch defaults to repo default branch, configurable via --base-branch flag)
   b. Run per-repo setup command if configured (e.g. npm install).
   c. Start Claude Code in the worktree with the worker CLAUDE.md
      and the task description as the initial prompt.
   d. db.ClaimTask(id, sessionID)
3. For each active task:
   a. Check if the Claude Code process is still alive.
   b. If exited cleanly (code 0): db.DoneTask(id), tear down worktree.
   c. If exited uncleanly: run triage flow.
4. Log state summary to ~/.dispatch/dispatch.log.
```

#### Process management

Workers run as child processes. The daemon tracks PID → task ID mapping in memory (not in the database — this is ephemeral orchestration state that dies with the daemon). A PID file is written to `~/.dispatch/worktrees/<id>/worker.pid` on spawn to bridge daemon restarts.

On daemon startup:
1. Call db.ListTasks("active", false) to get all active tasks.
2. For each, check if a worktree exists at `~/.dispatch/worktrees/<id>`.
3. If worktree exists, read `worker.pid` and check if the process is alive.
4. If alive: re-adopt into PID map, resume monitoring.
5. If dead: block the task (Phase 2) or run triage flow (Phase 3+).
6. If no worktree or PID file: block the task with "unknown state".

#### Triage flow

When a worker exits non-zero:

1. db.BlockTask(id, "Worker exited with code <N>").
2. Capture last 100 lines of the worker's stdout/stderr (logged to `~/.dispatch/sessions/<id>.log`).
3. Capture `git log --oneline -10` and `git diff --stat` from the worktree.
4. Spawn a short-lived Claude Code session with the triage CLAUDE.md, passing:
   - The task description
   - The captured log tail
   - The git state
5. The triage agent uses `dt` CLI commands (it's a Claude Code session, not Go code):
   - Commits partial work, adds a note with `dt note`, runs `dt reopen <id>` → daemon spawns a fresh worker.
   - Adds a detailed note explaining what went wrong, leaves the task blocked → human deals with it.
6. Tear down the triage session.

If you don't want automated triage (reasonable initially), skip step 4-5 and just leave the task blocked with the exit code and log tail as the block reason. Add triage later.

#### Worktree cleanup

**Standalone tasks (no parent):** On `dt done`: `git worktree remove ~/.dispatch/worktrees/<id>` and delete the branch.

**Child tasks (Phase 3+):** On completion, the daemon merges the child branch into the parent plan branch, then removes both the worktree and child branch. On merge conflict, the task is blocked and the worktree and branch are preserved for human resolution.

On triage that reopens: same cleanup, fresh worktree will be created on next spawn.

#### Concurrency

**Phase 2:** CLI flags with environment variable fallbacks (`--max-workers`/`DISPATCH_MAX_WORKERS`, default 4). No config file.

**Phase 5:** Config file at `~/.dispatch/config.toml` with per-repo settings:

```toml
max_workers = 4

[repos.myproject]
path = "~/projects/myproject"
setup_cmd = "npm install"
test_cmd = "npm test"
```

The daemon reads this on startup and on SIGHUP.

#### Session logging

Each worker's stdout/stderr is tee'd to `~/.dispatch/sessions/<id>.log`. This serves double duty: human debugging and triage agent context.

**Phase 2:** Output captured in-memory only (last 100 lines ring buffer) for crash block reasons. Disk logging deferred to Phase 3 when the triage agent needs full session logs as context.

---

### 2.3 Prompt files

Three markdown files, not code.

#### `worker.md`

Injected into each worker's Claude Code session via `--system-prompt` or equivalent.

Core instructions:
- Your task ID is $TASK_ID. Run `dt show $TASK_ID` to read your assignment. Note: `dt show --json` returns `{"task": {...}, "notes": [...], "blockers": [...], "blocking": [...], "children": [...]}`.
- Read notes from any tasks that blocked you (context from previous work).
- Implement the task. Run tests as described in the task description.
- When done: commit with message referencing the task ID, run `dt done $TASK_ID`, exit.
- When stuck: run `dt block $TASK_ID "<what you tried and what decision is needed>"`, exit.
- Do not create new tasks. Do not modify other tasks. Do not manage git branches.
- Add notes with `dt note $TASK_ID --author $SESSION_ID` to document non-obvious decisions for future workers.

#### `triage.md`

Injected into triage sessions.

Core instructions:
- A worker has crashed. Assess the damage.
- You receive: task description, log tail, git state.
- Check: is the worktree clean? Are there useful partial changes?
- If recoverable: commit partial work with message explaining state, add a note summarizing what was done and what remains, run `dt reopen $TASK_ID`.
- If not recoverable: add a note with structured diagnosis (what was attempted, what failed, what a human should look at), leave the task blocked.
- Do not attempt large-scale fixes. You are assessing, not implementing.

#### `dispatch-planner` skill

Not a daemon component. A superpowers skill (`/dispatch-planner`) you invoke in a Claude Code session after brainstorming produces an approved spec. It replaces `writing-plans` when targeting dispatch for execution.

See `docs/superpowers/specs/2026-03-21-dispatch-planner-design.md` for the full design.

Core behavior:
- Reads the spec and relevant codebase areas.
- Presents a parallelism rationale (what's parallel, what's sequential, why) for user approval.
- Proposes a task list with descriptions (what/scope/footprint/verification/context) and dependency wiring.
- Dispatches a reviewer subagent to check for overlapping files, oversized tasks, missing deps, underspecified scope.
- Creates a parent task + children atomically via `dt batch` with back-references (`$1`, `$2`, etc.).
- Tasks sized by scope coherence and decision density, not hard line counts. Heuristic: >10 files or >300 lines of non-trivial logic = consider splitting.
- Does not implement anything. Planning only.

---

## 3. Deferred Features (Future Phases)

These are intentionally excluded from the initial build but tracked here as candidates for future phases. Each should be gated on real usage confirming the need.

### Likely (Phase 5-6 candidates)

- **TUI.** A lightweight terminal UI for monitoring workers, viewing the task tree, and attaching to sessions. Deferred because `dt list --tree` and `watch` cover the basics. Build when you know what information you actually look at during a session. If added, likely needs a socket protocol or database polling with efficient diffing.
- **Multi-repo routing.** Per-repo config, repo field on tasks, daemon routes workers to correct worktree root. Deferred because single-repo covers most workflows. Add when you're actively working across repos simultaneously.
- **FTS search on tasks.** Full-text search over task titles, descriptions, and notes. kbtz uses FTS5 for smart `claim-next` ranking. Add when the task count is high enough that `dt list | grep` doesn't cut it.
- **Socket protocol / event streaming.** Required for a responsive TUI and for external tooling that wants live updates. Deferred because SQLite WAL handles CLI + daemon concurrency for now. Add alongside the TUI.
- **Task templates / structured descriptions.** Enforced fields in task descriptions (file list, test command, approach). The planner prompt suggests this structure but doesn't enforce it. Add if workers consistently struggle with underspecified tasks.

### Possible (evaluate after sustained use)

- **Persistent agent identity.** Named agents with history across sessions. Gas Town does this. Useful if you want agents to accumulate context or specialization. Likely overkill for <10 concurrent workers.
- **Git sync / JSONL export.** Portability of task state across machines. Deferred because this is a single-machine tool. Add if you want to plan on one machine and execute on another (e.g., laptop → home server).
- **Branch-aware task state.** Tasks fork and merge with git branches. Beads' core feature. Deferred because a global task database is simpler and sufficient for daemon-orchestrated work. Revisit if you find yourself wanting different task views per branch.
- **Web dashboard.** Visual overview of worker state. Add if the TUI doesn't cover the monitoring need or you want remote access.

### Explicitly not planned

- **Multi-user support.** Single developer tool.
- **Gas Town abstractions** (formulas, convoys, molecules, hooks, mail). Solving problems at a scale (20-30 agents) we're not targeting.
- **Persistent REPL / Docker isolation.** No language-specific warm-up needs. Git worktrees provide sufficient isolation.

---

## 4. Design Choices and Rationale

### Why roll our own task tracker?

Beads brings Dolt, git sync, formulas, compaction, federation, and a rapidly moving API (58 releases). We'd use 20% of it and be exposed to breaking changes. kbtz is solid but unmaintained (1 star, 1 contributor). The task tracker is ~500-800 lines. The schema is the only hard part and kbtz has proven the design.

### Why no socket protocol?

SQLite with WAL mode supports one writer and multiple readers concurrently. The daemon writes (claim, done, block). The CLI writes (add, edit, dep, note). Contention is near-zero because writes are fast single-row updates with a 5-second busy timeout. A socket protocol adds a serialization layer, a wire format, connection management, and backward compatibility concerns — all to solve a problem WAL already solves.

### Why 4-char hex IDs?

65,536 possible IDs. You'll never have more than a few hundred tasks alive at once. Short IDs are fast to type and easy to reference in conversation. If you ever hit collisions (you won't), extend to 5 chars.

### Why not use Linear/GitHub Issues?

Network dependency. API rate limits. Latency on every state transition. Credential management. The daemon needs to transition task state on the order of milliseconds, not seconds. A local SQLite database is ~100µs per write.

### Why `status IN ('open','active','blocked','done')` and not more states?

Dispatch's PRD has Triage as a separate status. But triage is a transient process, not a resting state — a task enters triage and within seconds comes out either as 'open' (recovered) or 'blocked' (needs human). There's no point where a human looks at a task and sees "Triage" as useful information. The block reason and notes carry the triage context. Four states is the minimum that captures the actual lifecycle.

### Why triage is optional in v1?

Automated triage burns tokens and may not be reliable enough initially. The simplest version: worker crashes → task gets blocked with exit code and log tail → human looks at it. That's useful from day one. Add the triage agent after you've seen enough crashes to know what recovery patterns are common.

### Why no multi-repo support?

One repo simplifies everything: one worktree root, one setup command, one test command. The config is three lines. Multi-repo means routing tasks to repos, per-repo concurrency limits, per-repo config, and workers that need to know which repo they're in. Add it when you need it, which is when you're actually working across repos simultaneously.

### Why the daemon reads task ready instead of watching for events?

Polling every 5 seconds is simple, predictable, and impossible to get wrong. Event-driven means inotify on the database file, or triggers, or a notification channel — all more complex than a sleep loop for negligible latency improvement. A 5-second delay between task readiness and worker spawn is imperceptible in a workflow where tasks take minutes.

---

## 5. Implementation Order

### Phase 1: Task CLI

Build the `dt` binary. All commands, --json support, cycle detection, `dt ready`, `dt batch`. Test with manual task creation and querying. This is useful standalone even without the daemon — you can use it in a Claude Code session with planner.md immediately.

**Exit criteria:** Create 10 tasks with dependencies. `dt ready` returns them in correct order. `dt batch` creates a set atomically. All commands produce correct JSON.

### Phase 2: Daemon (spawn + monitor)

Build `dispatchd` with the main loop: poll ready → create worktree → spawn Claude Code → monitor → detect exit → done or block. No triage agent yet — crashed workers just get blocked with log context.

**Exit criteria:** Create a task manually. Start daemon. Worker spawns in a worktree, implements the task, calls `dt done`, daemon tears down worktree. Kill a worker with SIGKILL. Daemon detects death, blocks the task with log tail.

### Phase 3: Planner, triage agent, and prompt files

Phase 3 adds the three agent roles and the merge model that makes the planner usable:

- **`dispatch-planner` skill** — decomposes specs into parallel task graphs with dependency wiring. Creates parent task + children via `dt batch` with back-references.
- **`worker.md` system prompt** — injected into worker sessions (currently empty in `ClaudeSpawner`).
- **`triage.md` system prompt** — injected into triage sessions on worker crash.
- **Session logging to disk** (`~/.dispatch/sessions/<id>.log`) — full session context for the triage agent.
- **Triage flow** — on worker crash, spawn a short-lived Claude session with triage.md and crash context.
- **Parent task / merge model:**
  - Parent tasks (tasks with children) are excluded from `ReadyTasks` and never get workers.
  - Child worktrees branch from the parent's plan branch (`dispatch/plan-<id>`), created lazily on first child spawn.
  - On child completion, the daemon merges the child branch into the parent branch before marking done. Merge conflicts block the task, preserving the branch and worktree for human resolution.
  - Parent auto-completes when all children are done. The parent branch is the PR branch.
- **`dt batch` back-references** — `$1`, `$2` syntax so the planner can create parent + children + deps atomically.

**Exit criteria:**
- Planner decomposes a spec into ≥5 tasks with dependencies. Parent branch accumulates completed work via merge. Dependent tasks see blocker's merged work when they start. Merge conflict blocks the task. Parent auto-completes when all children done. Parent branch is PR-ready.
- Worker crashes on a task with partial work committed. Triage agent assesses, commits partial work, reopens. Fresh worker picks up and completes.
- Worker sessions are logged to disk. Worker receives the `worker.md` system prompt.

### Phase 4: Polish from use

Use the system for real work for a week. Note what's missing. Likely candidates: better `dt list` formatting, status filtering, `dt search` with text matching, prompt tuning for worker and triage agents.

**Exit criteria:** Used for real work across 5+ sessions. Friction points documented. Worker clean exit rate >80%.

### Phase 5: Multi-repo support (when needed)

Add repo field to tasks. Per-repo config with path, setup command, test command. Daemon routes workers to correct worktree root based on task's repo field. Per-repo concurrency limits.

**Exit criteria:** Tasks for two different repos coexist in the database. Workers spawn in correct repo worktrees. Per-repo concurrency respected.

### Phase 6: TUI + socket protocol (when needed)

Lightweight terminal UI showing live worker state, task tree, and session attachment. Requires adding a Unix socket or similar for the daemon to push state updates. TUI is a client that connects to the daemon — closing it has no effect on workers.

**Exit criteria:** Open TUI while workers are running. All state visible. Attach to a worker session, detach. Close TUI, workers unaffected. Reopen, state correct.

### Future phases (evaluate from experience)

- **Chunks (epics).** Phase 3's parent task / merge model partially addresses this — a parent task groups children and its branch accumulates completed work into a PR-ready branch. The full chunk concept adds: merge agents for automated conflict resolution, `dt merge-chunk <id>` command, full test suite runs before PR creation, and architectural context that individual task descriptions shouldn't duplicate. The current model uses a shared parent branch where all children merge sequentially; a more sophisticated model where branch topology mirrors the dependency graph (each task branches from its dependency's completed branch, merges only at convergence points) is documented as a deferred design option.
- FTS search on tasks
- Git sync / JSONL export for cross-machine portability
- Task templates with structured description fields
- Persistent agent identity across sessions
- Branch-aware task state

---

## 6. Phase 1 Implementation Notes

Decisions made during Phase 1 that inform later phases:

- **`internal/db` package with `queryable` interface.** The `DB` struct uses an interface satisfied by both `*sql.DB` and `*sql.Tx`. All methods (AddTask, ClaimTask, ReadyTasks, etc.) work transparently in both contexts. `BeginTx()` returns a `*DB` whose queries run inside the transaction. This means `dispatchd` imports the same package and gets full type-safe access to all operations.
- **`dt show --json` returns an envelope.** Shape: `{"task": {...}, "notes": [...], "blockers": [...], "blocking": [...], "children": [...]}`. Worker and triage agents that parse this output need to account for the nesting.
- **Empty arrays serialize as `[]`, not `null`.** `dt ready --json` and `dt list --json` return `[]` when there are no results.
- **`dt note` has an `--author` flag** (default "human"). Workers should use `--author $SESSION_ID` to identify which agent wrote each note.
- **Status transitions create system notes.** Every call to ClaimTask, DoneTask, BlockTask, etc. appends a note with `author="system"` and content like "Status changed: open → active". These appear in `dt show` output and provide status history without a separate table.
- **Module path:** `github.com/dispatch-ai/dispatch`. Both `dt` and `dispatchd` binaries live in `cmd/`.

## 8. Phase 2 Implementation Notes

Decisions made during Phase 2 that inform later phases:

- **`WorkerSpawner` interface with `Done()` channel.** The PRD described `WorkerHandle` with just `PID()`, `Wait()`, and `Output()`. The implementation adds `Done() <-chan struct{}` and `Err() error` for non-blocking exit detection. `monitorWorkers()` uses `select` on `Done()` instead of polling `isProcessAlive()` — this is necessary because PID-based liveness checks don't work for test doubles. The `Done()` channel is closed when the process exits; `Err()` returns the exit error after `Done()` is closed. Both `claudeHandle` and `adoptedHandle` implement this pattern.
- **`ClaudeSpawner` is separate from `MockSpawner`.** `MockSpawner` lives in `mock_spawner.go` (not a `_test.go` file) because integration tests in the `tests` package need to import it. `ClaudeSpawner` is in `claude_spawner.go`.
- **Re-adopted workers check task status before blocking.** The `adoptedHandle` always reports "status unknown" on exit since we can't get the real exit code from a process we didn't spawn. `monitorWorkers()` checks if the task is already `done` (worker called `dt done` before exiting) before deciding to block. This prevents incorrectly blocking workers that completed successfully while the daemon was down.
- **`worker.md` system prompt not loaded yet.** `ClaudeSpawner.SystemPrompt` is empty string with a TODO. Phase 3 needs to load the `worker.md` file and pass its contents. Consider a `--worker-prompt` flag or reading from a conventional path like `~/.dispatch/worker.md`.
- **No config file.** Configuration is CLI flags with env var fallbacks (`--max-workers`/`DISPATCH_MAX_WORKERS`, `--db`/`DISPATCH_DB`, `--base-branch`/`DISPATCH_BASE_BRANCH`, `--repo`/`DISPATCH_REPO`, `--poll-interval`/`DISPATCH_POLL_INTERVAL`). Config file deferred to Phase 5.
- **Session logging deferred.** Worker output is captured in-memory only (100-line ring buffer) for crash block reasons. Phase 3 needs to add disk logging (`~/.dispatch/sessions/<id>.log`) for the triage agent to read full session context.
- **Orphaned worktree cleanup runs every poll cycle.** The daemon scans `~/.dispatch/worktrees/` and removes directories that don't correspond to active tasks. This handles edge cases like daemon crashes mid-spawn.
- **`ClaimTask` is not atomic.** The DB layer's `ClaimTask` does a read-then-write (check assignee is nil, then update). Two concurrent daemons could theoretically double-claim. This is acceptable for single-daemon use but would need a `WHERE assignee IS NULL` atomic update for multi-daemon scenarios.
- **On unclean exit, branch is preserved.** Worktree is removed but the `dispatch/<task_id>` branch is kept for human inspection. On clean exit, both worktree and branch are removed.

## 10. Phase 3 Implementation Notes

Decisions made during Phase 3 that inform later phases:

- **`dt batch` back-references.** `$1`, `$2`, etc. in batch input are substituted with the task ID returned by the corresponding earlier `add` command (1-indexed). This allows the planner to create parent + children + deps in a single atomic transaction. Note: `$N` patterns inside quoted strings (descriptions, titles) are also substituted — the planner should avoid literal `$N` in task descriptions.
- **Parent branch naming convention.** `dispatch/plan-<parentID>` for parent plan branches. Created lazily by the daemon when the first child task is ready to spawn, branched from the base branch.
- **Merge-on-completion ordering.** For child tasks, the daemon merges the child branch into the parent branch *before* calling `DoneTask`. This ensures dependent tasks (which wait for `status = 'done'` via `ReadyTasks`) always see the blocker's work on the parent branch when they spawn. On merge conflict, the task is blocked without ever being marked done.
- **Parent auto-completion.** `DoneTask` recursively checks if all siblings are done and auto-completes the parent. Errors from the recursive call are propagated to the caller.
- **Parent tasks excluded from `ReadyTasks`.** Any task with children is excluded via `NOT EXISTS (SELECT 1 FROM tasks child WHERE child.parent_id = t.id)`. The daemon never spawns workers for parent tasks.
- **Parent plan branches are safe from orphan cleanup.** `cleanOrphanedWorktrees` only removes worktree directories, not bare branches. Parent plan branches have no worktree directory, so they are never cleaned up. This is an implicit invariant — if orphan cleanup is ever changed to also clean branches, parent plan branches must be explicitly excluded.

## 11. Language

Go. SQLite via `mattn/go-sqlite3` (CGO). CLI via `cobra`. Single binary for `dt`, single binary for `dispatchd`.

Go. SQLite via `mattn/go-sqlite3` (CGO). CLI via `cobra`. Single binary for `dt`, single binary for `dispatchd`.
