# Phase 1 Design: `dt` CLI Task Tracker

**Date:** 2026-03-20
**Status:** Approved
**PRD Reference:** dispatch-prd_1.md, Phase 1

---

## Overview

`dt` is a single Go binary that reads and writes a SQLite database to track tasks. It is the foundation for the Dispatch orchestration system — both humans and agents use the same CLI commands. Phase 1 delivers all CLI commands, `--json` support, cycle detection, and batch execution.

---

## Project Structure

```
go.mod
cmd/dt/main.go              — entry point, cobra root command
cmd/dt/commands/add.go       — dt add
cmd/dt/commands/edit.go      — dt edit
cmd/dt/commands/dep.go       — dt dep, dt undep
cmd/dt/commands/claim.go     — dt claim, dt release
cmd/dt/commands/status.go    — dt done, dt block, dt reopen
cmd/dt/commands/note.go      — dt note
cmd/dt/commands/query.go     — dt ready, dt list, dt show
cmd/dt/commands/batch.go     — dt batch
internal/db/db.go            — database init, migrations, pragmas
internal/db/tasks.go         — task CRUD operations
internal/db/deps.go          — dependency operations + cycle detection
internal/db/notes.go         — notes operations
internal/id/id.go            — 4-char hex ID generation
```

## Dependencies

- **Go** (1.22+)
- **`github.com/mattn/go-sqlite3`** — SQLite driver (CGO)
- **`github.com/spf13/cobra`** — CLI framework

Module path: `github.com/dispatch-ai/dispatch`

## Database

**Location:** `~/.dispatch/dispatch.db` (override with `--db <path>` or `DISPATCH_DB` env var). Directory is created automatically if it doesn't exist.

**Pragmas** (set on every connection):
- `journal_mode = WAL`
- `busy_timeout = 5000`
- `foreign_keys = ON`

### Schema

```sql
CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL DEFAULT 'open'
                CHECK (status IN ('open','active','blocked','done')),
    block_reason TEXT,
    assignee    TEXT,
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
    author      TEXT,
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
```

Tables are created on first use (CREATE IF NOT EXISTS).

### ID Generation

4-character lowercase hex string, randomly generated. On insert, check for collision and regenerate if needed. 65,536 possible values — collisions are near-impossible at expected scale but the check is one line.

## Commands

All commands support `--json` for machine-readable output.

| Command | Description |
|---------|-------------|
| `dt add <title> [-d desc] [-p parent] [--after blocker]` | Create a task, print new ID |
| `dt edit <id> [-t title] [-d desc]` | Update title or description |
| `dt dep <blocker> <blocked>` | Add dependency, reject cycles |
| `dt undep <blocker> <blocked>` | Remove dependency |
| `dt claim <id> <assignee>` | Set active + assignee. Fail if already claimed |
| `dt release <id>` | Set open, clear assignee |
| `dt done <id>` | Set done, clear assignee |
| `dt block <id> <reason>` | Set blocked + reason, clear assignee |
| `dt reopen <id>` | Set open, clear block_reason + assignee |
| `dt note <id> [content]` | Append note. Stdin if content omitted |
| `dt ready` | List unclaimed, unblocked, open tasks (priority order) |
| `dt list [--tree] [--all] [--status s]` | List tasks. Hides done by default |
| `dt show <id>` | Full task detail with notes + deps |
| `dt batch` | Read commands from stdin, execute in one transaction |

### Cycle Detection

On `dt dep <blocker> <blocked>`: DFS from `blocked` through the dependency graph. If `blocker` is reachable, the dependency would create a cycle — reject with error.

### `dt ready` Query

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

### `dt batch`

Reads one command per line from stdin. All commands execute in a single SQLite transaction. Any error rolls back the entire batch. Lines use the same syntax as regular commands (without the `dt` prefix).

### `--json` Output

Each command returns a Go struct. Default output is human-readable (formatted tables for lists, key-value pairs for single items). `--json` marshals the struct as JSON instead.

## Architecture Decisions

- **Business logic in `internal/db`** — command files are thin wrappers that parse flags, call db functions, and format output.
- **No ORM** — direct SQL with `database/sql`. The schema is simple enough that an ORM adds complexity without value.
- **`updated_at` trigger** — set via application code on writes, not a database trigger. Simpler.
- **Global `--db` flag** on the root cobra command, threaded through to all subcommands.

## Testing

Integration tests against a real SQLite database (temp file per test). No mocks. Tests exercise the CLI through the Go functions in `internal/db`, covering:

1. Create 10 tasks with dependencies — `dt ready` returns correct order
2. `dt batch` creates a set atomically
3. All commands produce correct JSON output
4. Cycle detection rejects circular dependencies
5. State transitions: claim → release, claim → done, block → reopen
6. `dt list --tree` renders hierarchy correctly
7. `dt show` includes notes, deps, and children

## Exit Criteria (from PRD)

- Create 10 tasks with dependencies
- `dt ready` returns them in correct order
- `dt batch` creates a set atomically
- All commands produce correct JSON
