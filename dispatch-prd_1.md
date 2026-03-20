# PRD: Dispatch

**Codename:** Dispatch  
**Author:** Tal  
**Date:** 2026-03-19  
**Status:** Draft v1

---

## 1. Problem Statement

Juliatown solved a specific problem: autonomous AI development in a Julia monorepo with warm REPL persistence and a tmux-based workflow. It works well for that use case but is tightly coupled to Julia, Docker, and a single repository. Building on those lessons, Dispatch generalizes the architecture into a repo-independent, daemon-first multi-agent development system.

Three problems remain after Juliatown:

1. **Task ledger fragility.** `progress.json` is written freehand by Claude agents with no schema enforcement, no state machine, and no locking. Agents occasionally write malformed JSON or make invalid state transitions. A real task ledger with schema enforcement is strictly better.

2. **Docker overhead is no longer justified.** The Docker layer in Juliatown existed to isolate Julia compilation and warm a persistent REPL. Without Julia, Docker adds complexity with no compensating benefit. Git worktrees provide branch isolation at essentially zero cost.

3. **The system requires tmux literacy.** Juliatown is tmux all the way down. Dispatch inverts this: the daemon runs the system, a TUI provides the primary interface, and tmux is an invisible execution substrate the user never touches directly.

### 1.1 What we are building

A daemon-first multi-agent development system where:

- A persistent background daemon (`dispatchd`) owns all mechanical coordination: polling Linear for ready issues, managing git worktrees, spawning and monitoring Claude Code worker processes.
- Workers are short-lived Claude Code sessions, one per Linear issue, running in a git worktree. They read their issue, implement it, commit, and close it.
- An **Architect agent** handles high-level decomposition of large bodies of work (PRDs, large feature requests) into chunks. It uses Claude Code Agent Teams to spawn coordinated Planner teammates, enabling Planners to communicate directly with each other to resolve interface conflicts and maintain cross-chunk coherence — without routing everything through the Architect.
- A **Mayor agent** is a thin conversational interface. You describe work, it routes to Architect or Planner as appropriate, and handles judgment calls. It has no operational management responsibility.
- The **TUI** (`dispatch tui`) is an optional, freely openable and closable interface that connects to the running daemon via Unix socket. Closing the TUI does not affect running workers.
- The **CLI** (`dispatch`) provides the same control surface as the TUI for scripting and quick operations.
- **Linear** is the source of truth for all task state. The daemon never manages its own task ledger.

### 1.2 What we are NOT building

- A general-purpose multi-agent framework
- A Julia-specific tool (Dispatch is language-agnostic)
- A Docker management layer (git worktrees replace containers)
- A tmux-native interface (tmux is internal plumbing, not the UI)
- Multi-user support (this is single-developer)
- Persistent agent identity (agents are ephemeral, state is in Linear)

---

## 2. User Stories

### 2.1 Start new work from a focused idea (single-chunk)

> "I have a focused idea — fix the auth module. I open the Mayor. It asks a couple of scoping questions, determines this is single-chunk work, and spawns a standalone Planner. The Planner reads the relevant code and writes Linear issues. The daemon picks them up and starts workers."

**Acceptance criteria:**
- Mayor asks <=3 clarifying questions and determines single vs. multi-chunk scope
- Planner creates Linear issues with blocking relationships
- Daemon detects new ready issues within 10 seconds and spawns workers
- Total time from idea to first worker executing: <5 minutes

### 2.2 Start new work from a PRD (multi-chunk)

> "I hand the Mayor a PRD. It determines this needs multi-chunk decomposition and spawns the Architect. The Architect reads the PRD, uses Agent Teams to spin up a Planner per chunk. Planners coordinate directly with each other on shared interfaces, then each writes their chunk's Linear issues. I see workers start appearing as issues become ready."

**Acceptance criteria:**
- Mayor correctly routes PRD-scale work to the Architect rather than a single Planner
- Architect spawns a Claude Code Agent Team with one Planner teammate per chunk
- Planners communicate directly (not through Architect) to resolve cross-chunk interface conflicts
- All Planners write to Linear before the Architect session exits
- Daemon respects inter-chunk dependency ordering from blocking relationships
- Total time from PRD to first worker executing: <15 minutes

### 2.3 Monitor running work

> "I open `dispatch tui`. I see workers running across two repos. One issue shows blocked. I trigger investigate from the TUI. A triage agent runs and either fixes it or surfaces a diagnosis."

**Acceptance criteria:**
- TUI shows all active workers with repo, issue title, and progress
- Blocked and failed issues are visually distinct with reason visible
- Investigate can be triggered from the TUI without leaving it
- TUI updates reflect daemon state within 2 seconds via socket

### 2.4 Unclean worker exit

> "A worker crashes. The daemon detects it, transitions the Linear issue to Triage, and a short-lived triage agent reads the worktree and git state. If it can resume cleanly it does; otherwise it appends a diagnosis to the issue, transitions to blocked, and I see a notification."

**Acceptance criteria:**
- Daemon detects unclean exit within 10 seconds
- Triage agent runs, assesses worktree state, attempts recovery
- On success: issue re-queued, fresh worker spawned
- On failure: issue transitions to blocked with structured diagnosis, notification in TUI/CLI

### 2.5 Worker-initiated block

> "A worker determines the approach is wrong. It calls `dispatch block` with a reason and exits cleanly. I see it in the TUI and either edit the issue and reopen it, or open the Mayor to re-plan."

**Acceptance criteria:**
- `dispatch block` available inside worker sessions
- Issue transitions to blocked in Linear with reason attached
- Human can reopen (with edited description) from TUI, CLI, or Mayor
- Daemon spawns fresh worker on reopen

### 2.6 Work across multiple repos

> "I ask the Mayor to work on my Rust project and my Python project. Workers spawn in worktrees of the correct repos. The TUI shows workers labeled by repo."

**Acceptance criteria:**
- Mayor reads per-repo config to determine worktree location and repo-specific worker instructions
- Daemon uses repo field on Linear issues to route to the correct worktree root
- Workers in different repos run concurrently without interference

### 2.7 Recover from daemon restart

> "I restart my machine. I run `dispatchd`. It reads Linear for in-progress issues, finds live tmux sessions for still-running workers, reconnects, and resumes monitoring."

**Acceptance criteria:**
- `dispatchd` on startup queries Linear for all non-terminal issues
- Running tmux sessions are detected and reconciled with Linear state
- Workers that exited while daemon was down are triaged
- TUI reconnects via socket within 5 seconds of daemon restart

---

## 3. Architecture

### 3.1 System Overview

```
Linear (source of truth)
  Issues: Todo -> In Progress -> Done / Blocked / Triage
  Blocking relationships encode dependency graph
  Repository field routes issues to correct worktree

dispatchd (always-running daemon)
  Polls Linear every 5s for ready issues
  Manages worker lifecycle: spawn, monitor, triage
  Serves state over Unix socket (~/.dispatch/dispatch.sock)
  Writes activity log (~/.dispatch/activity.log)

Architect (on-demand, for multi-chunk work)
  Claude Code session launched by Mayor
  Uses Agent Teams: one Planner teammate per chunk
  Planners communicate peer-to-peer on shared interfaces
  Commits issues to Linear chunk-by-chunk for crash resilience
  Exits when all chunks have issues in Linear

Planner (Agent Team teammate, or standalone for single-chunk)
  Reads repo code, writes Linear issues with blocking relationships
  In team context: coordinates directly with sibling Planners
  Exits after writing issues

Workers (ephemeral Claude Code sessions)
  One per Linear issue, in a git worktree
  Hosted in a managed tmux session (invisible to user)
  Exit on completion or explicit block

Mayor (on-demand conversational interface)
  Routes to Architect (multi-chunk) or Planner (single-chunk)
  Handles blocked issues, re-planning, judgment calls
  Stateless between invocations

Triage agent (spawned by daemon on unclean exit)
  Assesses worktree state, attempts minimal recovery
  Flags in Linear if non-recoverable

dispatch CLI  -- connects to daemon socket
dispatch tui  -- connects to daemon socket, freely openable/closable
```

### 3.2 Agents

| Agent | Runs on | Lifecycle | Spawned by | Primary tools |
|-------|---------|-----------|------------|---------------|
| Mayor | Host (any terminal) | On-demand, stateless | User (`disp mayor`) | Linear MCP, dispatch CLI |
| Architect | Host (tmux session) | Per planning session | Mayor or `dispatch plan --prd` | Agent Teams, Linear MCP, file read |
| Planner (teammate) | Host (Agent Team) | Per chunk, within Architect session | Architect via Agent Teams | Linear MCP, file read, peer messaging |
| Planner (standalone) | Host (tmux session) | Per chunk, single-chunk work | Mayor or `dispatch plan --chunk` | Linear MCP, file read |
| Worker | Host (tmux session via daemon) | Per Linear issue | dispatchd | Linear MCP, dispatch CLI, git |
| Triage agent | Host (tmux session via daemon) | Per failed issue | dispatchd | Linear MCP, git, dispatch CLI |

### 3.3 Planning Flow: Single-Chunk vs. Multi-Chunk

The Mayor determines routing based on scope:

**Single-chunk** (focused task, one area of code):
```
User -> Mayor: "fix the auth module"
Mayor: asks <=3 clarifying questions
Mayor: dispatch plan --chunk "auth-fix" "<brief>"
  -> spawns standalone Planner session
  -> Planner reads code, writes Linear issues
  -> Planner exits
dispatchd: detects new ready issues, spawns workers
```

**Multi-chunk** (PRD, large feature, cross-cutting change):
```
User -> Mayor: attaches PRD
Mayor: determines multi-chunk scope
Mayor: dispatch plan --prd "<brief>"
  -> spawns Architect session (CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1)
  -> Architect reads PRD, decomposes into N chunks
  -> Architect establishes shared interface boundaries in conversation
  -> Architect spawns Agent Team: one Planner teammate per chunk
  -> Planners read their chunk's code in parallel
  -> Planners message each other directly to resolve interface conflicts
  -> Each Planner writes its chunk's Linear issues
  -> Architect does coherence review, wires cross-chunk blocking relationships
  -> Architect exits
dispatchd: detects new ready issues, spawns workers in dependency order
```

### 3.4 Key Invariants

1. **Linear is the only task ledger.** Workers never write to progress files; they call the Linear API.
2. **The daemon owns worker lifecycle.** Nothing spawns or kills workers except `dispatchd` (or explicit CLI commands).
3. **Workers are single-issue.** A worker reads one Linear issue, implements it, and exits.
4. **The Mayor has no operational responsibility.** It writes to Linear and spawns planning sessions; the daemon acts on what Linear says.
5. **Closing the TUI changes nothing.** The daemon and all workers continue running.
6. **All state reconstructable from Linear.** Kill the daemon at any time; restart and it re-derives state from Linear.
7. **The Architect is planning-only.** It never writes code, never interacts with worktrees, and exits when issues are in Linear.

### 3.5 Failure Handling

#### Unclean worker exit

1. Daemon detects process exit with non-zero status
2. Transitions Linear issue from In Progress → Triage
3. Spawns triage agent with: issue contents, worktree git log, last N lines of worker session
4. Triage agent attempts recovery: checks if changes are clean, assesses resumability
5. On success: commit partial work, transition to Todo, daemon spawns fresh worker
6. On failure: append structured diagnosis to Linear issue, transition to Blocked, emit notification to socket

#### Worker-initiated block

Worker calls `dispatch block <issue-id> "<reason>"` and exits with code 0. Issue transitions to Blocked in Linear. Resolution: human edits issue and calls `dispatch reopen`, or routes through Mayor for re-planning.

#### Intervention paths

- `dispatch investigate <issue-id>` — manually triggers triage agent for any Blocked or Triage issue
- `dispatch reopen <issue-id>` — transitions Blocked → Todo; daemon spawns fresh worker
- **TUI** — blocked/triage issues shown with inline reason; investigate and reopen available without leaving TUI
- **Mayor** — reads Linear state, can rewrite the issue, re-plan the chunk, or spawn an Architect for full re-decomposition

#### Architect session failure

If an Architect session dies mid-planning, any issues already committed to Linear remain (Architect commits chunk-by-chunk). Unfinished chunks have no issues and the daemon ignores them. The user reruns `dispatch plan --prd`; the Architect detects which chunks already have issues and skips them.

---

## 4. Component Specifications

### 4.1 dispatchd — Daemon

A single long-running Go binary. Manages all worker lifecycle and serves the control socket.

**Startup:**
- Read config (`~/.dispatch/config.toml`)
- Open Unix socket at `~/.dispatch/dispatch.sock`
- Query Linear for all non-terminal issues
- Detect existing tmux sessions matching dispatch naming convention
- Reconcile: mark workers dead if session gone, triage if they were In Progress
- Begin main loop

**Main loop (every 5 seconds):**
1. Query Linear: issues in Todo state with no open blockers, up to parallelism limit
2. For each ready issue not already running: create worktree, spawn worker session, transition to In Progress
3. For each running issue: check tmux session liveness
4. For any dead session: trigger triage flow
5. Emit state snapshot to all connected socket clients

**Socket protocol:** JSON over Unix socket. Daemon pushes state snapshots on every monitor tick. Clients send commands (stop, pause, resume, investigate, reopen). Request/response for commands, streaming for state updates. Version field in protocol; TUI shows warning on mismatch.

**Parallelism:** Configurable max concurrent workers per repo and globally. Defaults: 5 global, 2 per repo.

### 4.2 Architect Agent

A Claude Code session launched for multi-chunk planning. Uses Claude Code Agent Teams (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`) to spawn one Planner teammate per chunk. The Architect is the team lead; Planners are teammates.

**CLAUDE.md functional spec:**

- On start: read the PRD or brief. Identify the chunks — independent workstreams that can be planned in parallel.
- Establish shared interface boundaries before spawning Planners: types, API contracts, naming conventions, shared modules. Write a brief interface spec in the conversation.
- Spawn an Agent Team: one teammate per chunk. Each teammate receives: the chunk brief, the interface spec, and instructions to message sibling Planners directly if they encounter a conflict or shared concern.
- Monitor Planner progress via team task list. Nudge stuck Planners.
- Commit each chunk's issues to Linear as the Planner for that chunk finishes — don't batch all at once.
- When all Planners have written their issues: do a coherence review — check that cross-chunk blocking relationships are correctly wired, no two chunks are touching the same issue, shared interfaces match.
- Fix any coherence issues by messaging the relevant Planner or making direct Linear API corrections.
- Exit when all issues are committed and coherence review passes.

**Agent Teams limitations to design around:**

- All agents (Architect + Planners) run on Opus. This is a known cost: multi-chunk planning with Agent Teams is expensive. Accepted given it replaces hours of manual planning, and is a short-lived session.
- No nested teams: Planners cannot spawn sub-teams. If a chunk is large enough to warrant further decomposition, the Planner flags it and the Architect re-decomposes before committing.
- No session resumption: if the Architect session dies, planning must restart from where it left off. Chunk-by-chunk Linear commits reduce the blast radius.
- Per-role model selection (Opus for Architect, Sonnet for Planners) is not yet supported but is a community feature request — adopt when available to reduce planning cost significantly.

### 4.3 Planner Agent (standalone)

Used for single-chunk work spawned directly by the Mayor. Identical behavior to a Planner teammate but runs as a standalone Claude Code session.

**CLAUDE.md functional spec:**

- Read the chunk brief.
- Explore relevant repo code: `find`, `grep`, read files.
- Propose a plan as a list of issues with descriptions and blocking relationships. Iterate with user if interactive.
- Issue sizing: each issue touches <=8 files, <=200 lines of changes. Split larger tasks.
- Write structured issue descriptions: exact files, approach, test commands to verify.
- Create all issues in Linear via MCP. Wire dependencies in a second pass. Exit.

### 4.4 Mayor Agent

A Claude Code session launched with `disp mayor`. Routes work to Architect or Planner, handles judgment calls.

**Routing heuristic:**
- **Single-chunk:** a focused task, one area of code. Use `dispatch plan --chunk`.
- **Multi-chunk:** a PRD, large feature touching multiple independent areas, cross-cutting refactor. Use `dispatch plan --prd`.
- When in doubt: ask one clarifying question about whether there are multiple independent workstreams.

**CLAUDE.md functional spec:**

- On startup: run `dispatch status`. Summarize active workers, blocked issues, recent activity.
- On new work: ask <=3 clarifying questions. Route to Architect or standalone Planner.
- On blocked issue: read block reason from Linear. Either rewrite the issue (`dispatch reopen`) or spawn a Planner to re-scope.
- On triage notification: read diagnosis. Decide whether to reopen with different instructions or abandon.
- Never read source code directly (that is Planner/Architect work). Never manage worktrees.

### 4.5 Workers

A Claude Code process running in a managed tmux session, in a git worktree for its issue's branch.

**Lifecycle:**
1. Daemon creates git worktree: `git worktree add ~/.dispatch/worktrees/<issue-id> -b dispatch/<issue-id>`
2. Daemon creates tmux session: `dispatch-<issue-id>`
3. Daemon starts Claude Code with worker CLAUDE.md and issue ID as initial prompt
4. Worker reads its Linear issue, implements, commits, closes via `dispatch close <issue-id>`, exits 0
5. Daemon detects exit, tears down worktree, logs activity

**Worker CLAUDE.md functional spec:**

- On start: call `dispatch read <issue-id>`. Read any context from closed blocking issues.
- Implement the issue. Run tests as specified in the issue or repo config.
- When done: commit with message referencing the issue ID. Call `dispatch close <issue-id>`. Exit 0.
- When blocked: call `dispatch block <issue-id> "<reason including what was tried and what decision is needed>"`. Exit 0.
- Never manage progress files. Never write to Linear directly (use dispatch CLI). Never spawn subagents for coordination.

### 4.6 Triage Agent

Short-lived Claude Code session spawned by the daemon on unclean worker exit. Read-mostly.

**CLAUDE.md functional spec:**

- Receive: issue contents, git log, last 100 lines of worker session output, list of modified files.
- Assess: is the worktree clean? What was the worker doing? Is partial work usable?
- If trivially recoverable: commit partial work, call `dispatch reopen <issue-id>`, exit.
- If not recoverable: write structured diagnosis (what was done, what failed, what decision is needed). Call `dispatch block <issue-id> "<diagnosis>"`. Exit.
- Never attempt large-scale fixes. Triage is assessment and minimal recovery only.

### 4.7 Linear Structure

One Linear workspace shared across all repos. Issues carry a `Repository` field that the daemon uses to route to the correct worktree root.

**Issue schema:**

| Field | Type | Purpose |
|-------|------|---------|
| Title | string | Short description of the task |
| Description | text | Detailed implementation instructions for the worker |
| Status | enum | Todo, In Progress, Done, Blocked, Triage |
| Repository | custom field | Repo name, maps to path in config |
| Branch | custom field | Git branch for the worktree (auto-set by daemon) |
| Block reason | custom field | Set by worker or triage agent on block |
| Blocking / Blocked by | relation | Dependency graph; daemon filters on no open blockers |

**Status state machine:**

```
Todo -> In Progress        (daemon, on spawn)
In Progress -> Done        (worker, on close)
In Progress -> Blocked     (worker, on block)
In Progress -> Triage      (daemon, on unclean exit)
Triage -> Todo             (triage agent, on recovery)
Triage -> Blocked          (triage agent, on non-recoverable)
Blocked -> Todo            (human or Mayor, on reopen)
```

### 4.8 Per-Repo Config

A minimal config file per repo, committed to the repo at `.dispatch/config.toml`:

```toml
[repo]
name = "my-project"             # must match Linear Repository field value
path = "~/projects/my-project"  # absolute or ~ path to repo root

[worker]
setup_cmd = "npm install"       # run once after worktree creation (optional)
test_cmd = "npm test"           # surfaced to workers as the test command

[worker.env]
NODE_ENV = "test"
```

Global config at `~/.dispatch/config.toml`:

```toml
[dispatch]
max_workers = 5
max_workers_per_repo = 2

[[repos]]
name = "my-project"
path = "~/projects/my-project"

[[repos]]
name = "other-project"
path = "~/projects/other-project"
```

---

## 5. TUI (`dispatch tui`)

An optional BubbleTea terminal UI. Connects to the running daemon via Unix socket. Can be opened and closed at any time without affecting running workers. Carries over Catppuccin Frappé theming, sidebar, tab bar, and status overlay from the juliatown TUI.

### 5.1 Layout

```
+── Sidebar (28col) ──+── Tab bar ──────────────────────────────────────────+
│                     │  [M] mayor  [A] prd-planning  [W] auth-fix          │
│ DISPATCH            +─────────────────────────────────────────────────────+
│                     │                                                     │
│ WORKERS             │   [terminal preview of active tab session]          │
│ ● auth-fix    3/7   │   (press enter to attach)                          │
│ ◌ db-migrate  BLK   │                                                     │
│ ● api-refactor 1/5  │                                                     │
│                     +── Status overlay ───────────────────────────────────+
│ ACTIVITY            │ ── auth-fix ● ████░░ 3/7 ── db-migrate ◌ BLK ──    │
│ 14:32 auth: ✓ 3     +─────────────────────────────────────────────────────+
│ 14:28 db: ✗ 1       +── Bottom bar ───────────────────────────────────────+
│ 14:15 api: ✓ 1      │ [enter] attach  [p] pause  [i] investigate  [n] new │
│                     +─────────────────────────────────────────────────────+
│ 2● 1◌  0⏸           │
+─────────────────────+
```

### 5.2 Sidebar

Fixed 28-column left pane. Shows all active workers grouped by status, rolling activity feed, and summary counts. Highlights the worker corresponding to the active tab. Reads from daemon state snapshots via socket — no direct Linear or filesystem polling.

### 5.3 Tab Bar

Tabs for: Mayor session (if running), Architect session (if running), each active Planner (during planning), each active worker, any triage sessions. Tabs are discovered from daemon state, not manually managed.

Tab type icons: Mayor `[M]`, Architect `[A]`, Planner `[P]`, Worker `[W]`, Triage `[T]`.

### 5.4 Terminal Preview

The content area shows a live capture of the active tab's tmux session via `tmux capture-pane`. Read-only. Press Enter to attach (`ReleaseTerminal` → `tmux attach-session` → `RestoreTerminal` on detach). Same attach/detach pattern as juliatown TUI.

### 5.5 Status Overlay

A single line between content and bottom bar showing all workers inline with mini progress bars. Truncates gracefully if too wide.

### 5.6 Keybindings

| Key | Action | Condition |
|-----|--------|-----------|
| `enter` | Attach to active tab session | Tab has a live tmux session |
| `tab` / `shift+tab` | Switch tabs | Always |
| `p` | Pause worker | Active tab is a running worker |
| `r` | Resume worker | Active tab is a paused worker |
| `s` | Stop worker | Active tab is a worker |
| `i` | Investigate issue | Active tab is blocked or triage |
| `o` | Reopen issue | Active tab is blocked |
| `n` | New chunk (text input overlay) | Always |
| `q` / `ctrl+c` | Quit TUI (daemon keeps running) | Always |
| `x` | Dismiss exited tab | Active tab is exited or error |

### 5.7 Daemon Connection

On startup, TUI connects to `~/.dispatch/dispatch.sock`. If the daemon is not running it shows a clear message and retries every 2 seconds. On disconnect it shows a reconnecting indicator rather than crashing. The TUI never starts the daemon itself — `dispatchd` is the user's responsibility to keep running (launchd/systemd service recommended).

---

## 6. CLI (`dispatch`)

All commands connect to the daemon socket. If the daemon is not running most commands fail with a clear error. Exception: `dispatch start`.

| Command | Description |
|---------|-------------|
| `dispatch start` | Start the daemon (if not running). |
| `dispatch stop-daemon` | Stop the daemon and all running workers gracefully. |
| `dispatch status` | Print current state: workers, blocked issues, recent activity. |
| `dispatch tui` | Open the TUI. |
| `dispatch mayor` | Launch a Claude Code session with the Mayor prompt. |
| `dispatch plan --chunk "<brief>"` | Launch a standalone Planner for a single-chunk task. |
| `dispatch plan --prd "<brief>"` | Launch an Architect session for multi-chunk decomposition. |
| `dispatch stop <issue-id>` | Stop the worker for an issue and transition to Todo. |
| `dispatch pause <issue-id>` | Pause the worker process. |
| `dispatch resume <issue-id>` | Resume a paused worker. |
| `dispatch block <issue-id> "<reason>"` | Transition issue to Blocked with reason. Called by workers. |
| `dispatch close <issue-id>` | Transition issue to Done. Called by workers on completion. |
| `dispatch investigate <issue-id>` | Spawn triage agent for a Blocked or Triage issue. |
| `dispatch reopen <issue-id>` | Transition Blocked → Todo; daemon spawns fresh worker. |
| `dispatch read <issue-id>` | Print issue contents. Called by workers. |
| `dispatch logs [--follow]` | Show activity log. `--follow` streams new entries. |
| `dispatch worktrees` | List all active worktrees managed by the daemon. |

---

## 7. Milestones

### M1: Daemon skeleton + Linear integration

**Goal:** `dispatchd` runs, polls Linear, manages basic worker spawn/monitor loop.

- `dispatchd` binary: starts, reads config, opens socket
- Linear client: query ready issues, transition states, read/write custom fields
- Worker spawn: git worktree add, tmux session create, claude process start
- Worker monitor: session liveness check, clean exit detection
- `dispatch status` CLI command working

**Exit criteria:** Create a Linear issue manually. Run `dispatchd`. Issue transitions to In Progress, tmux session appears with Claude Code running in a worktree. Issue closes when Claude finishes.

### M2: Failure handling + triage

**Goal:** Unclean exits and worker-initiated blocks handled correctly.

- Unclean exit detection and Linear transition to Triage
- Triage agent spawn with correct context
- `dispatch block` and `dispatch investigate` CLI commands
- Notification emission over socket
- Reopen flow: Blocked → Todo → fresh worker

**Exit criteria:** `kill -9` a running worker. Triage agent spawns, either recovers or flags in Linear. `dispatch investigate` works on a blocked issue.

### M3: Mayor + standalone Planner

**Goal:** End-to-end workflow for single-chunk work.

- Mayor CLAUDE.md: reads Linear state, routes to Planner, handles blocked issues
- Planner CLAUDE.md: reads repo, generates Linear issues with dependencies
- `dispatch mayor` and `dispatch plan --chunk` CLI commands
- Parallelism limits and dependency-aware scheduling in daemon

**Exit criteria:** Describe a focused task to Mayor → Planner generates issues → daemon spawns workers in dependency order → issues close.

### M4: Architect + Agent Teams

**Goal:** Multi-chunk planning from PRD with coordinated Planners.

- Architect CLAUDE.md: reads PRD, decomposes chunks, spawns Agent Team
- Planner teammate CLAUDE.md: peer-to-peer coordination variant
- `dispatch plan --prd` CLI command
- Coherence review pass in Architect before exiting
- Incremental commit to Linear (chunk-by-chunk) for crash resilience

**Exit criteria:** Hand a PRD to Mayor → Architect spawns Planner team → Planners coordinate on shared interfaces → all issues written to Linear → workers execute in dependency order.

### M5: TUI

**Goal:** `dispatch tui` works as a full control surface.

- BubbleTea app connecting to daemon socket
- Sidebar, tab bar, terminal preview, status overlay, bottom bar
- Architect and Planner tabs visible during planning sessions
- All keybindings: attach, pause, resume, stop, investigate, reopen, new chunk
- Catppuccin Frappé theming carried over from juliatown

**Exit criteria:** Open `dispatch tui` while workers are running. All state visible. Attach to a worker session, detach, close TUI. Workers keep running. Reopen TUI, state still correct.

### M6: Multi-repo + polish

**Goal:** Production-quality for daily use across multiple repos.

- Per-repo config reading and worktree routing
- launchd/systemd service config for `dispatchd`
- `dispatch worktrees` cleanup command
- Activity log retention and rotation

**Exit criteria:** Use Dispatch for real work across two repos for 5 consecutive days without manual intervention beyond the Mayor.

---

## 8. Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Agent Teams experimental limitations (no session resumption, all-Opus cost) | High | Medium | Architect commits issues chunk-by-chunk. Agent Teams used only for short planning sessions. Accepted cost for coherence benefit. |
| Agent Teams deprecated or changed before Dispatch ships | Low | Medium | Architect designed so Planner coordination could fall back to sequential standalone Planners with a shared interface doc if Agent Teams is unavailable. |
| Linear API rate limits | Low | Medium | 5s poll interval keeps request rate low. Cache last state snapshot in daemon. |
| Linear API downtime | Low | High | Daemon pauses spawning on API error. Running workers finish. Retry with backoff. |
| Git worktree conflicts | Low | Low | Daemon uses issue-id-scoped branch names: `dispatch/<issue-id>`. Collisions essentially impossible. |
| Triage agent makes things worse | Medium | Medium | Constrained to assessment + minimal recovery only. CLAUDE.md explicitly prohibits large-scale fixes. |
| Worker overshoots issue scope | High | Low | Planner sizing rule (<=8 files, <=200 lines). Triage detects oversized commits and flags. |

---

## 9. Success Metrics

After 2 weeks of daily use:

- **Time from idea to first worker executing:** <5 minutes (single-chunk), <15 minutes (multi-chunk from PRD)
- **Worker clean exit rate:** >85% of issues close without triage
- **Triage auto-recovery rate:** >60% of unclean exits recovered without human intervention
- **Cross-chunk coherence:** no interface mismatches discovered during worker execution for Architect-planned work
- **Daemon uptime between intentional restarts:** >48 hours
- **TUI open/close cycle:** <1 second, zero effect on running workers

---

## 10. Non-Goals and Future Considerations

**Explicitly out of scope for v1:**
- Multi-user support
- CI/CD integration (workers run tests locally; CI is separate)
- Automatic PR creation (Mayor can offer, human confirms)
- Windows support (macOS + Linux)
- GUI of any kind
- Persistent agent identity
- Per-chunk model selection in Agent Teams (not yet supported)

**Things to watch:**
- Agent Teams stability and session resumption — adopt improvements as they land
- Per-role model selection in Agent Teams (community feature request) — when available, run Planners on Sonnet to reduce planning cost significantly
- Claude Code orchestration features generally — adopt anything that simplifies Architect or Worker design

---

## Appendix A: Why not Docker

Juliatown used Docker to solve two problems: Julia warm REPL persistence (cold startup 10-30 minutes) and process isolation for running Claude with elevated permissions. Without Julia, warm REPL persistence is not needed. Process isolation is a deployment concern: if you want a sandboxed environment, run the entire Dispatch stack inside a container. The system does not need to know.

Git worktrees provide the branch isolation that actually matters: each worker gets its own working tree, its own HEAD, and cannot interfere with other workers' in-progress changes.

---

## Appendix B: Relationship to Juliatown

| Concern | Juliatown | Dispatch |
|---------|-----------|----------|
| Task ledger | `progress.json` (freehand JSON) | Linear (schema-enforced, state machine) |
| Worker isolation | Docker container per chunk | Git worktree per issue |
| Worker scope | One worker per chunk (multi-issue loop with subagents) | One worker per issue (single task, exits) |
| Multi-chunk planning | Not supported | Architect + Agent Teams: coordinated Planners |
| Coordinator | Mayor Claude session + bash daemon | `dispatchd` Go daemon (deterministic, no tokens) |
| UI | tmux session as the UI | TUI connects to daemon, tmux is internal plumbing |
| Repo scope | Single Julia monorepo | Multiple repos, routed by Linear field |
| Context handoff | Informal, CLAUDE.md convention | Linear issue description + block reason (structured) |

The TUI, sidebar design, Catppuccin theming, terminal preview/attach pattern, and tab bar carry over from juliatown's Go TUI with minimal changes. The juliatown TUI codebase is the direct starting point for M5.
