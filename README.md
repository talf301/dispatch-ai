# Dispatch

A minimal task tracker and orchestration daemon for coordinating multiple Claude Code agents on a single developer's machine. Two binaries, one SQLite database, a few prompt files.

## What It Does

1. **Track tasks** — create, decompose, depend, claim, close, block
2. **Run workers** — pick ready tasks, spawn Claude Code in git worktrees, detect completion or failure
3. **Review gates** — automatic code review before merge
4. **GraphPilot integration** — sync task completion back to your planning graph

## Components

### `dt` — CLI Task Tracker

A single binary that reads and writes a SQLite database. Agents and humans use the same commands.

```
dt add <title> [-d <desc>] [-p <parent>] [--after <blocker>] [-r <repo>]
dt edit <id> [-t <title>] [-d <desc>] [-r <repo>]
dt dep <blocker> <blocked>       # Add dependency (cycle-checked)
dt undep <blocker> <blocked>     # Remove dependency
dt claim <id> <assignee>         # Claim a task
dt release <id>                  # Release a claim
dt done <id>                     # Mark complete
dt block <id> <reason>           # Block with reason
dt reopen <id>                   # Reopen a task
dt note <id> <content>           # Add a note [--author <author>]
dt ready                         # List unclaimed, unblocked tasks
dt list [--tree] [--all] [--status <s>] [--json]
dt show <id> [--json]
dt batch                         # Execute multiple commands atomically
dt init <repo-path>              # Register a repo
```

All commands support `--json` for machine-readable output. Task IDs are 4-character hex strings for ergonomic CLI use.

### `dispatchd` — Orchestration Daemon

Long-running daemon that polls every 5 seconds:

1. Find ready tasks (unclaimed, unblocked, dependencies met)
2. Spawn Claude Code workers in isolated git worktrees
3. Monitor for completion or crash
4. On success: run reviewer, merge branch, mark done, tear down worktree
5. On crash: block task with log context for human triage
6. Parent tasks auto-complete when all children finish

#### Review Gate

Workers don't self-certify. On clean exit, the daemon spawns a read-only reviewer that checks spec compliance and code quality. Approved work is merged; rejected work is reopened with a note explaining why.

#### Multi-Repo Support

Per-repo configuration in `~/.dispatch/config.toml` with independent worker limits. Tasks carry an optional `repo` field for routing.

## GraphPilot Integration

Dispatch integrates with [GraphPilot](../graph-pilot) to keep your planning graph in sync with execution progress.

**Enable:** `dispatchd --gp` or `DISPATCH_GP=1`

### How It Works

- **On task completion** — daemon fires `gp sync-child <task-id>` to update the corresponding GraphPilot node
- **On batch dispatch** — `dt batch` auto-wires the GP graph when `GRAPHPILOT_NODE` is set, calling `gp dispatch <node> --plan <parent-task-id>`
- **Graceful fallback** — if `gp` isn't in PATH, warns and continues without error

This creates a closed loop: plan in GraphPilot, decompose into Dispatch tasks, execute with workers, and sync completion back to the graph.

## Quick Start

```bash
# Build
go build -o dt ./cmd/dt
go build -o dispatchd ./cmd/dispatchd

# Initialize a repo
dt init /path/to/your/repo

# Add tasks
dt add "Implement auth middleware" -d "JWT validation for API routes" -r my-repo
dt add "Write auth tests" --after <auth-task-id> -r my-repo

# Start the daemon
dispatchd                  # basic
dispatchd --gp             # with GraphPilot sync
```

## Architecture

```
~/.dispatch/
  dispatch.db    SQLite database (WAL mode)
  config.toml    Per-repo settings (max_workers, etc.)
  dispatch.log   Daemon activity log
```

```
dispatch-ai/
  cmd/
    dt/           CLI commands
    dispatchd/    Daemon entry point
  internal/
    daemon/       Worker spawning, monitoring, review gates, GP hooks
    db/           SQLite wrapper (tasks, deps, notes)
    config/       Config file parsing
    id/           4-char hex ID generation
  prompts/
    worker.md     Worker agent instructions
    reviewer.md   Reviewer agent instructions
```

## Dependencies

- Go 1.22+
- SQLite (via `go-sqlite3`)
- Cobra (CLI framework)
- TOML (config parsing)
