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
  Issues: Todo -> In Progress -> In Review -> Done / Blocked / Triage
  Blocking relationships encode dependency graph
  Repository field routes issues to correct worktree

dispatchd (always-running daemon)
  Polls Linear adaptively (5s eager / 30s idle / immediate on notify)
  Manages worker lifecycle: spawn, monitor, review, triage
  Runs post-worker review pipeline (test gate + AI review)
  Merges completed issue branches into chunk integration branches
  Auto-creates draft PRs on chunk completion
  Serves state over Unix socket (~/.dispatch/dispatch.sock)
  Writes activity log (~/.dispatch/activity.log)

Architect (on-demand, for multi-chunk work)
  Claude Code session launched by Mayor
  Decomposes PRD into chunks, spawns Planners (sequential or Agent Teams)
  Collects oppositional design review feedback across chunks
  Presents consolidated review to user for approval
  Commits issues to Linear chunk-by-chunk after approval
  Exits when all chunks have issues in Linear

Planner (Agent Team teammate, or standalone for single-chunk)
  Reads repo code, writes plan doc (.dispatch/plans/<chunk>.md)
  Spawns oppositional design review (simplicity + completeness)
  Writes Linear issues only after user approves plan
  Exits after writing issues

Workers (ephemeral Claude Code sessions, Sonnet default)
  One per Linear issue, in a git worktree
  Hosted in a managed tmux session (invisible to user)
  Uses superpowers plugin (TDD, verification, systematic debugging)
  Exit on completion or explicit block

Review pipeline (per worker completion)
  Test gate: repo test suite must pass
  AI review agent (Sonnet): spec compliance, scope, defects
  Critical issues reject closure; non-critical noted on issue

Mayor (on-demand conversational interface)
  Routes to Architect (multi-chunk) or Planner (single-chunk)
  Handles blocked issues, re-planning, judgment calls
  Stateless between invocations

Triage agent (spawned by daemon on unclean exit, Sonnet)
  Assesses worktree state, attempts minimal recovery
  Flags in Linear if non-recoverable
  Circuit breaker: 3 triage cycles → Blocked unconditionally

dispatch CLI  -- connects to daemon socket
dispatch tui  -- connects to daemon socket, freely openable/closable
```

### 3.2 Agents

| Agent | Runs on | Model | Lifecycle | Spawned by | Primary tools |
|-------|---------|-------|-----------|------------|---------------|
| Mayor | Host (any terminal) | Opus | On-demand, stateless | User (`dispatch mayor`) | Linear MCP, dispatch CLI |
| Architect | Host (tmux session) | Opus | Per planning session | Mayor or `dispatch plan --prd` | Agent Teams, Linear MCP, file read |
| Planner (teammate) | Host (Agent Team) | Opus (Agent Teams constraint) | Per chunk, within Architect session | Architect via Agent Teams | Linear MCP, file read, peer messaging |
| Planner (standalone) | Host (tmux session) | Opus | Per chunk, single-chunk work | Mayor or `dispatch plan --chunk` | Linear MCP, file read |
| Design Reviewer | Host (subagent) | Sonnet | Per plan review (2 per plan) | Planner | file read |
| Worker | Host (tmux session via daemon) | Sonnet (default) | Per Linear issue | dispatchd | Linear MCP, dispatch CLI, git, superpowers |
| Review agent | Host (subagent via daemon) | Sonnet | Per worker completion | dispatchd | git diff, issue read |
| Triage agent | Host (tmux session via daemon) | Sonnet | Per failed issue | dispatchd | Linear MCP, git, dispatch CLI |

### 3.3 Planning Flow: Single-Chunk vs. Multi-Chunk

The Mayor determines routing based on scope:

**Single-chunk** (focused task, one area of code):
```
User -> Mayor: "fix the auth module"
Mayor: asks <=3 clarifying questions
Mayor: dispatch plan --chunk "auth-fix" "<brief>"
  -> spawns standalone Planner session
  -> Planner reads code, writes plan doc to .dispatch/plans/<chunk-name>.md
  -> Planner spawns oppositional design review (simplicity vs. completeness)
  -> Review feedback presented to user in Mayor session
  -> User approves (with optional edits) or requests changes
  -> Planner writes Linear issues from approved plan
  -> Planner exits
dispatchd: detects new ready issues, spawns workers
```

**Multi-chunk** (PRD, large feature, cross-cutting change):
```
User -> Mayor: attaches PRD
Mayor: determines multi-chunk scope
Mayor: dispatch plan --prd "<brief>"
  -> spawns Architect session
  -> Architect reads PRD, decomposes into N chunks
  -> Architect establishes shared interface boundaries
  -> Architect spawns Planners (sequential or Agent Teams)
  -> Each Planner reads its chunk's code, writes plan doc
  -> Each Planner spawns oppositional design review
  -> Architect collects review feedback across all chunks
  -> Review feedback presented to user (via Mayor or TUI)
  -> User approves (with optional edits) or requests changes
  -> Planners write Linear issues from approved plans
  -> Architect does coherence review, wires cross-chunk blocking relationships
  -> Architect exits
dispatchd: detects new ready issues, spawns workers in dependency order
```

### 3.4 Branch Integration Strategy

Workers operate in isolated worktrees, but dependent issues need access to their predecessors' changes. Dispatch uses **integration branches** scoped per chunk to solve this.

**How it works:**

1. When a Planner creates issues for a chunk (e.g. "auth-fix"), the daemon creates an integration branch: `dispatch/chunk/auth-fix`, forked from main.
2. Issue 1 (no blockers) → worktree branches from `dispatch/chunk/auth-fix`. Worker commits to `dispatch/issue-1`, closes.
3. Daemon merges `dispatch/issue-1` into `dispatch/chunk/auth-fix` (fast-forward when possible, merge commit otherwise).
4. Issue 2 (blocked by issue 1) becomes ready → worktree branches from `dispatch/chunk/auth-fix` (which now contains issue 1's work).
5. Repeat until all issues in the chunk are done. `dispatch/chunk/auth-fix` contains the complete chunk's work.

**Merge conflicts:** If merging an issue branch into the chunk integration branch conflicts, the daemon transitions the issue to Triage with diagnosis "merge conflict with integration branch". The triage agent attempts a simple resolution; if it can't, the issue goes to Blocked for human intervention.

**Concurrent workers within a chunk:** When two issues in the same chunk have no dependency between them, the daemon may run them in parallel. Both branch from the same integration branch snapshot. When both complete, the first merge succeeds, but the second may conflict — even though the Planner declared the issues independent. The Planner cannot perfectly predict file-level conflicts. The daemon handles this with a **merge queue**: within a chunk, only one merge into the integration branch happens at a time. If the second worker's branch conflicts after the first merge lands, it goes through the normal triage flow (merge conflict diagnosis). To reduce the frequency of this: the Planner should flag when two independent issues touch overlapping files, and the daemon should prefer serial execution for issues within the same chunk that lack explicit dependency but share file paths (detectable from the issue description's "files touched" field).

**Cross-chunk dependencies:** When an issue in chunk B depends on an issue in chunk A, the daemon merges chunk A's integration branch into chunk B's integration branch before spawning the dependent worker. If chunk A is not yet complete, only the specific dependency issue's branch is merged.

**PR flow:** When all issues in a chunk close, the chunk integration branch contains the complete, reviewed, tested work. The daemon automatically creates a draft PR from the integration branch (default behavior; disable with `auto_pr = false` in repo config). The PR description includes links to all issues, the plan doc, and any review feedback flagged during worker reviews. `dispatch pr <chunk-name>` can also be used to manually create a PR at any time.

### 3.5 Key Invariants

1. **Linear is the only task ledger.** Workers never write to progress files; they call the Linear API.
2. **The daemon owns worker lifecycle.** Nothing spawns or kills workers except `dispatchd` (or explicit CLI commands).
3. **Workers are single-issue.** A worker reads one Linear issue, implements it, and exits.
4. **The Mayor has no operational responsibility.** It writes to Linear and spawns planning sessions; the daemon acts on what Linear says.
5. **Closing the TUI changes nothing.** The daemon and all workers continue running.
6. **All state reconstructable from Linear.** Kill the daemon at any time; restart and it re-derives state from Linear.
7. **The Architect is planning-only.** It never writes code, never interacts with worktrees, and exits when issues are in Linear.
8. **Integration branches are the unit of merge.** Individual issue branches are never merged directly to main; they merge into their chunk's integration branch, which becomes the PR.

### 3.6 Failure Handling

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

#### Triage circuit breaker

The daemon tracks a `triage_count` per issue (stored as a Linear custom field to survive daemon restarts). Each time triage recovers an issue (Triage → Todo), the counter increments. If `triage_count >= 3`, the daemon skips auto-recovery entirely — the issue transitions directly to Blocked with reason "max triage attempts exceeded (3); requires human intervention". `dispatch reopen` resets the counter, since a human explicitly choosing to retry is a different signal than automated recovery looping.

#### Architect session failure

If an Architect session dies mid-planning, any issues already committed to Linear remain (Architect commits chunk-by-chunk). Unfinished chunks have no issues and the daemon ignores them. The user reruns `dispatch plan --prd`; the Architect detects which chunks already have issues and skips them.

### 3.7 Review Process

Review happens at three stages: before execution (plan review), during execution (per-worker), and after execution (chunk completion). Each stage catches different classes of problems.

#### Stage 1: Oppositional Design Review (before execution)

After a Planner writes a plan doc but **before** any Linear issues are created, the plan undergoes an oppositional design review. Two review subagents are spawned concurrently, each reading the same plan doc:

- **Simplicity Reviewer:** Argues for reducing scope. Looks for: over-engineering, unnecessary abstractions, features that could be deferred, issues that could be combined, dependencies that could be eliminated. Its bias is "what can we cut?"
- **Completeness Reviewer:** Argues for gaps. Looks for: missing error handling, untested edge cases, unaddressed integration points, implicit assumptions, missing migration steps. Its bias is "what did we miss?"

Each reviewer produces a short structured critique (max ~500 words): what they'd change and why. The two critiques are presented to the user together — the tension between them surfaces the real design decisions. The user then:
- Approves the plan as-is
- Provides specific feedback to incorporate
- Asks the Planner to revise and re-review

Only after approval does the Planner convert the plan into Linear issues. This prevents the most expensive failure mode: executing a bad plan across many workers.

**Approval interaction model:** The Planner is a Claude Code session running in a tmux session. After the design review completes, the Planner writes the review summary (both critiques plus its own synthesis) to the plan doc and then **prompts the user for input** in its own session. The user can interact with the Planner session directly (via TUI tab attach, or `tmux attach` to the Planner session). The Planner blocks on user input — it does not proceed until the user explicitly approves, requests changes, or cancels.

For the multi-chunk case, the Architect collects all Planner review summaries and presents them together when the user interacts with the Architect session. The user approves chunk-by-chunk or all-at-once.

If a Planner session is left waiting for approval for more than 1 hour, the daemon emits a notification (socket + activity log) reminding the user that a plan is awaiting review. The Planner does not time out — it waits indefinitely, since approval is a human decision.

Plan docs are saved to `.dispatch/plans/<chunk-name>.md` in the repo and committed. They serve as durable context for why decisions were made — workers can reference them, and future Planners can read past plans.

#### Stage 2: Post-Worker Review (per issue)

When a worker calls `dispatch close`, two things happen before the issue transitions to Done:

**A) Automated test gate.** The daemon runs the repo's test command (`test_cmd` from config) against the worker's branch. If tests fail, the closure is rejected — the worker's code is not merged into the integration branch. This is a hard gate: no code merges without passing tests.

For repos with slow test suites, two config options control the tradeoff between speed and safety:

```toml
[worker]
test_cmd = "npm test"                   # full suite (required)
test_cmd_fast = "npm test -- --related" # fast subset (optional, used on close)
test_cmd_full_on_merge = true           # run full suite before integration merge (default: true)
```

When `test_cmd_fast` is set, `dispatch close` runs the fast test command (e.g., tests related to changed files). The full suite runs once before the issue branch merges into the integration branch. This keeps the worker feedback loop tight (~30s for targeted tests) while still gating the integration branch on the full suite. If `test_cmd_fast` is not set, `test_cmd` is used for both.

**B) AI code review.** The daemon spawns a short-lived review agent (on Sonnet) that reads:
- The issue description (what was requested)
- The git diff (what was implemented)
- The test results

The review agent checks for:
- **Spec compliance:** does the diff match what the issue asked for?
- **Scope creep:** did the worker touch files or make changes beyond the issue's scope?
- **Obvious defects:** broken error handling, missing null checks, hardcoded values, security issues

If the review agent finds critical issues, the worker's closure is rejected — the issue stays In Progress and the review feedback is appended to the issue description. The worker (if still running) or a fresh worker picks up the feedback and addresses it. Non-critical feedback is noted on the issue but does not block closure.

The review agent runs on Sonnet to keep costs low (~$0.20-0.40 per review). At the default issue sizing (<=8 files, <=200 lines), the diff is small enough for a fast, focused review.

#### Stage 3: Chunk Completion (PR)

When all issues in a chunk are Done and merged into the integration branch, the daemon automatically creates a draft PR from the integration branch (default behavior; can be disabled with `auto_pr = false` in config). The PR description includes:
- Summary of all issues in the chunk with links
- The original plan doc
- Aggregate test results
- Any review feedback that was flagged during Stage 2

This is the human's opportunity for holistic review of the complete chunk before it merges to main.

### 3.8 Superpowers Integration

Dispatch integrates [superpowers](https://github.com/obra/superpowers) as the SWE practices layer for worker sessions. Superpowers enforces test-driven development, systematic debugging, and verification-before-completion through composable skills that are loaded into Claude Code sessions.

**What workers get from superpowers:**

- **Test-driven development:** Workers write failing tests before implementation. The TDD skill enforces a strict red-green-refactor cycle — code written before tests is rejected.
- **Verification before completion:** Workers must provide fresh evidence (test output, not assertions) before calling `dispatch close`. No "should work" or "probably passes."
- **Systematic debugging:** When a worker hits a problem, it follows a structured investigation cycle rather than guessing at fixes.

**What Dispatch handles (not superpowers):**

- Task decomposition and planning (Architect/Planner)
- Worker lifecycle and coordination (daemon)
- State management (Linear)
- Code review (dispatch review agents, not superpowers' code review skill — to avoid conflicts with the dispatch review pipeline)

**Configuration:** Workers are launched with superpowers installed as a Claude Code plugin. The worker CLAUDE.md references the relevant superpowers skills (TDD, verification, debugging) but not skills that conflict with Dispatch's own coordination (planning, code review, git worktrees). Per-repo config can disable superpowers for repos where TDD is not appropriate:

```toml
[worker]
superpowers = true          # default: true
superpowers_skills = ["test-driven-development", "verification-before-completion", "systematic-debugging"]
```

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

**Main loop (adaptive polling):**

The daemon polls Linear on an adaptive interval:
- **Eager mode (5s):** Active when any issues are In Progress, in Triage, or when a planning session has run in the last 60 seconds. This is the common case during active work.
- **Idle mode (30s):** Active when no issues are in flight. Reduces unnecessary API calls during quiet periods.
- **Immediate poll:** Workers and planners call `dispatch notify` after writing to Linear (closing, blocking, creating issues). This triggers an immediate poll cycle via the daemon socket, giving sub-second responsiveness for the transitions that matter most.

Each poll cycle:
1. Query Linear: issues in Todo state with no open blockers, up to parallelism limit
2. For each ready issue not already running: create worktree from chunk integration branch, spawn worker session, transition to In Progress
3. For each completed issue: merge issue branch into chunk integration branch
4. For each running issue: check tmux session liveness
5. For any dead session: trigger triage flow (subject to circuit breaker)
6. Emit state snapshot to all connected socket clients

**Socket protocol:** JSON over Unix socket. Daemon pushes state snapshots on every monitor tick. Clients send commands (stop, pause, resume, investigate, reopen). Request/response for commands, streaming for state updates. Version field in protocol; TUI shows warning on mismatch.

**Parallelism:** Configurable max concurrent workers per repo and globally. Defaults: 5 global, 2 per repo.

### 4.2 Architect Agent

A Claude Code session launched for multi-chunk planning. Uses Claude Code Agent Teams (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`) to spawn one Planner teammate per chunk. The Architect is the team lead; Planners are teammates.

**CLAUDE.md functional spec:**

- On start: read the PRD or brief. Identify the chunks — independent workstreams that can be planned in parallel.
- Establish shared interface boundaries before spawning Planners: types, API contracts, naming conventions, shared modules. Write a brief interface spec in the conversation.
- Spawn an Agent Team: one teammate per chunk. Each teammate receives: the chunk brief, the interface spec, and instructions to message sibling Planners directly if they encounter a conflict or shared concern.
- Monitor Planner progress via team task list. Nudge stuck Planners.
- When all Planners have written their plan docs: collect oppositional review feedback across all chunks.
- Present consolidated review feedback to user. Each chunk's simplicity and completeness critiques are shown together, plus any cross-chunk concerns the Architect identifies.
- Wait for user approval before any issues are written to Linear.
- On approval: Planners commit their plan docs and create Linear issues. Architect wires cross-chunk blocking relationships.
- Do a coherence review — check that cross-chunk blocking relationships are correctly wired, no two chunks are touching the same files, shared interfaces match.
- Fix any coherence issues by messaging the relevant Planner or making direct Linear API corrections.
- Exit when all issues are committed and coherence review passes.

**Agent Teams limitations to design around:**

- All agents (Architect + Planners) run on Opus. This is a known cost: multi-chunk planning with Agent Teams is expensive. Accepted given it replaces hours of manual planning, and is a short-lived session.
- No nested teams: Planners cannot spawn sub-teams. If a chunk is large enough to warrant further decomposition, the Planner flags it and the Architect re-decomposes before committing.
- No session resumption: if the Architect session dies, planning must restart from where it left off. Chunk-by-chunk Linear commits reduce the blast radius.
- Per-role model selection (Opus for Architect, Sonnet for Planners) is not yet supported but is a community feature request — adopt when available to reduce planning cost significantly.

**Sequential fallback (no Agent Teams):**

If Agent Teams is unavailable, unstable, or too expensive, the Architect falls back to sequential standalone Planners:

1. Architect decomposes PRD into N chunks and writes a shared interface spec to a file in the repo (`.dispatch/interface-spec.md`).
2. For each chunk sequentially: Architect calls `dispatch plan --chunk "<brief>"` with a reference to the interface spec.
3. Each standalone Planner reads the interface spec, plans its chunk, writes Linear issues.
4. After all Planners finish, Architect reads all created issues from Linear and does the coherence review / cross-chunk blocking wiring.

This loses parallelism and direct Planner-to-Planner communication but preserves the decomposition and coherence review. The interface spec file substitutes for live peer messaging. Build this first (M4a), then upgrade to Agent Teams (M4b) when confidence in the feature is high.

### 4.3 Planner Agent (standalone)

Used for single-chunk work spawned directly by the Mayor. Identical behavior to a Planner teammate but runs as a standalone Claude Code session.

**CLAUDE.md functional spec:**

- Read the chunk brief.
- Explore relevant repo code: `find`, `grep`, read files.
- **Scope check:** if during code exploration you discover the work requires multiple independent workstreams (e.g., changes across unrelated modules with no shared interface), call `dispatch escalate --to-architect "<what you found>"` instead of writing a plan. Do not try to plan multi-chunk work as a single chunk.
- Write a plan doc to `.dispatch/plans/<chunk-name>.md`. The plan contains:
  - Summary of the chunk's goal and approach
  - List of proposed issues with descriptions, files touched, and blocking relationships
  - Issue sizing: each issue touches <=8 files, <=200 lines of changes. Split larger tasks.
  - Test strategy per issue
- Spawn oppositional design review: two subagents reading the plan doc concurrently.
  - Simplicity reviewer: what can be cut, combined, or deferred?
  - Completeness reviewer: what's missing, implicit, or under-specified?
- Present review feedback to user (via Mayor session or directly if interactive). Wait for approval.
- On approval: create all issues in Linear via MCP. Wire dependencies in a second pass. Call `dispatch notify` after creating issues. Commit the plan doc. Exit.
- On revision request: update plan doc, re-run review if changes are substantial, re-present. Exit only after approval.

### 4.4 Mayor Agent

A Claude Code session launched with `disp mayor`. Routes work to Architect or Planner, handles judgment calls.

**Routing heuristic:**
- **Single-chunk:** a focused task, one area of code. Use `dispatch plan --chunk`.
- **Multi-chunk:** a PRD, large feature touching multiple independent areas, cross-cutting refactor. Use `dispatch plan --prd`.
- When in doubt: ask one clarifying question about whether there are multiple independent workstreams.
- **Misrouting is recoverable.** If a standalone Planner discovers the scope is larger than expected, it calls `dispatch escalate --to-architect "<findings>"` instead of writing issues. This kills the Planner session and spawns an Architect with the original brief plus the Planner's findings. The cost of one aborted Planner session is low; the cost of 30 issues in the wrong shape is high.

**CLAUDE.md functional spec:**

- On startup: run `dispatch status`. Summarize active workers, blocked issues, recent activity.
- On new work: ask <=3 clarifying questions. Route to Architect or standalone Planner.
- On blocked issue: read block reason from Linear. Either rewrite the issue (`dispatch reopen`) or spawn a Planner to re-scope.
- On triage notification: read diagnosis. Decide whether to reopen with different instructions or abandon.
- Never read source code directly (that is Planner/Architect work). Never manage worktrees.

### 4.5 Workers

A Claude Code process running in a managed tmux session, in a git worktree for its issue's branch.

**Lifecycle:**
1. Daemon creates git worktree from the chunk integration branch: `git worktree add ~/.dispatch/worktrees/<issue-id> -b dispatch/<issue-id> dispatch/chunk/<chunk-name>`
2. Daemon creates tmux session: `dispatch-<issue-id>`
3. Daemon installs superpowers plugin (if enabled) and starts Claude Code with worker CLAUDE.md and issue ID as initial prompt
4. Worker reads its Linear issue, implements with TDD, commits, calls `dispatch close <issue-id>`
5. `dispatch close` is a **blocking call**. The worker process remains alive while the daemon runs the review pipeline. The worker waits for the result.
6. Daemon runs test gate (repo test_cmd against worker branch). On failure: daemon rejects close, appends test output to issue, returns rejection to worker. Worker is expected to fix and re-close.
7. Daemon spawns review agent (Sonnet). On critical issues: daemon rejects close, appends review feedback to issue, returns rejection to worker. Worker reads feedback and addresses it.
8. On pass: issue transitions to Done, daemon merges issue branch into chunk integration branch, tears down worktree, logs activity.
9. **Worker timeout on close:** If the review pipeline takes longer than 5 minutes (configurable), `dispatch close` returns a timeout error. The issue transitions to In Review and the worker exits. On daemon's next cycle, if the review has completed, the issue transitions to Done or back to In Progress (with feedback). This prevents workers from hanging indefinitely on slow test suites.
8. When all chunk issues are Done: daemon creates draft PR from integration branch, emits notification

**Worker CLAUDE.md functional spec:**

- On start: call `dispatch read <issue-id>`. Read the plan doc (`.dispatch/plans/<chunk-name>.md`) for broader context. Read any context from closed blocking issues.
- Follow superpowers TDD skill: write failing tests first, then implement, then verify tests pass. Do not write implementation code before tests.
- Follow superpowers verification skill: before calling close, provide fresh test output as evidence. Never claim "should work" without proof.
- When done: commit with message referencing the issue ID. Call `dispatch close <issue-id>` — this is a **blocking call** that waits for the test gate + AI review to complete. If close returns success, exit 0.
- If `dispatch close` is rejected (test failure or critical review feedback): the rejection message includes the specific feedback. Read it, fix the issue, commit, and call `dispatch close` again. Do not exit until close succeeds or you determine the issue is unresolvable.
- If `dispatch close` times out: exit 0. The daemon will complete the review asynchronously and either transition the issue to Done or back to In Progress (spawning a fresh worker to address feedback).
- When blocked: call `dispatch block <issue-id> "<reason including what was tried and what decision is needed>"` (this also notifies the daemon). Exit 0.
- Never manage progress files. Never write to Linear directly (use dispatch CLI). Never spawn subagents for coordination.

**Worker guardrails:**

- **Session timeout:** The daemon kills workers that have been running longer than `worker_timeout` (default: 30 minutes, configurable). On timeout, the issue transitions to Triage. The triage agent assesses whether partial work is salvageable. Rationale: well-scoped issues (<=8 files, <=200 lines) should complete in 10-20 minutes on Sonnet. A 30-minute worker is likely stuck in a loop.
- **Max close attempts:** If a worker calls `dispatch close` and is rejected 3 times (test failure or review rejection), the daemon force-transitions the issue to Blocked with reason "max close attempts exceeded". This prevents workers from burning tokens in an infinite fix-and-retry loop.
- **Opus escalation:** If `opus_escalation = true` and a worker has been triaged twice (triage_count >= 2), the daemon respawns with `model = "opus"` before going to human-blocked. This is a single retry with a more capable model, not an open-ended escalation.

### 4.6 Triage Agent

Short-lived Claude Code session spawned by the daemon on unclean worker exit. Read-mostly.

**CLAUDE.md functional spec:**

- Receive: issue contents, git log, last 100 lines of worker session output, list of modified files.
- Assess: is the worktree clean? What was the worker doing? Is partial work usable?
- If trivially recoverable: commit partial work, call `dispatch reopen <issue-id>`, exit.
- If not recoverable: write structured diagnosis (what was done, what failed, what decision is needed). Call `dispatch block <issue-id> "<diagnosis>"`. Exit.
- Never attempt large-scale fixes. Triage is assessment and minimal recovery only.

### 4.7 Linear Structure

All Dispatch work lives in a **single Linear project** shared across all repos. Issues carry a `Repository` custom field that the daemon uses to route to the correct worktree root. Use Linear views (filtered by Repository) for per-repo visibility.

**Why one project, not per-repo projects:** Cross-repo blocking relationships work natively within a single project — no cross-project relation wiring. The daemon polls one project instead of N. The Architect can plan multi-repo work without coordinating across projects. The tradeoff is that Dispatch issues mix with any non-Dispatch work in the same project; if that becomes a problem, consider a dedicated "Dispatch" project or splitting to per-repo projects. Per-repo projects would require the daemon to poll multiple projects, and cross-repo dependencies would use Linear's cross-project relations (supported but less ergonomic).

**Issue schema:**

| Field | Type | Purpose |
|-------|------|---------|
| Title | string | Short description of the task |
| Description | text | Detailed implementation instructions for the worker |
| Status | enum | Todo, In Progress, In Review, Done, Blocked, Triage |
| Repository | custom field | Repo name, maps to path in config |
| Chunk | custom field | Chunk name; maps to integration branch `dispatch/chunk/<name>` |
| Branch | custom field | Git branch for the worktree (auto-set by daemon) |
| Block reason | custom field | Set by worker or triage agent on block |
| Triage count | custom field (number) | Incremented on each triage recovery; reset on human reopen |
| Blocking / Blocked by | relation | Dependency graph; daemon filters on no open blockers |

**Status state machine:**

```
Todo -> In Progress        (daemon, on spawn; worktree branched from chunk integration branch)
In Progress -> In Review   (worker, on close; triggers test gate + AI review)
In Review -> Done          (daemon, on review pass; merges issue branch into integration branch)
In Review -> In Progress   (daemon, on review reject; feedback appended to issue for worker to address)
In Progress -> Blocked     (worker, on block)
In Progress -> Triage      (daemon, on unclean exit; subject to circuit breaker)
Triage -> Todo             (triage agent, on recovery; triage_count incremented)
Triage -> Blocked          (triage agent, on non-recoverable; OR circuit breaker at triage_count >= 3)
Blocked -> Todo            (human or Mayor, on reopen; triage_count reset)
```

### 4.8 Per-Repo Config

A minimal config file per repo, committed to the repo at `.dispatch/config.toml`:

```toml
[repo]
name = "my-project"             # must match Linear Repository field value
path = "~/projects/my-project"  # absolute or ~ path to repo root
auto_pr = true                  # create draft PR on chunk completion (default: true)

[worker]
model = "sonnet"                # worker model (default: sonnet; "opus" for complex repos)
timeout_minutes = 30            # kill worker after this long (default: 30)
max_close_attempts = 3          # force-block after N rejected closes (default: 3)
setup_cmd = "npm install"       # run once after worktree creation (optional)
test_cmd = "npm test"           # full test suite, run before integration merge
test_cmd_fast = ""              # optional fast subset for close-time feedback (omit to use test_cmd)
superpowers = true              # install superpowers plugin in worker sessions (default: true)
superpowers_skills = ["test-driven-development", "verification-before-completion", "systematic-debugging"]
opus_escalation = true          # respawn with Opus after 2 triage cycles (default: true)

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
| `dispatch notify` | Signal daemon to poll Linear immediately. Called by workers/planners after Linear writes. |
| `dispatch escalate --to-architect "<findings>"` | Abort standalone Planner, spawn Architect with findings. Called by Planners on scope discovery. |
| `dispatch pr <chunk-name>` | Create a draft PR from the chunk's integration branch. |
| `dispatch logs [--follow]` | Show activity log. `--follow` streams new entries. |
| `dispatch worktrees` | List all active worktrees managed by the daemon. |

---

## 7. Cost Model

Workers are the dominant cost driver — they run most frequently and at scale. Model selection is the primary cost lever.

### 7.1 Per-Session Estimates

| Session type | Model | Avg tokens (in/out) | Est. cost | Frequency |
|---|---|---|---|---|
| Mayor | Opus | ~30K / ~5K | $1–2 | Per user interaction |
| Architect | Opus | ~100K / ~20K | $4–6 | Per PRD |
| Planner (standalone) | Opus | ~80K / ~15K | $2.50–4 | Per chunk |
| Design Reviewer (x2) | Sonnet | ~40K / ~3K each | $0.30–0.50 total | Per plan |
| Worker | Sonnet | ~50K / ~10K | $0.30–0.60 | Per issue (most frequent) |
| Review agent | Sonnet | ~30K / ~5K | $0.20–0.40 | Per issue completion |
| Triage agent | Sonnet | ~20K / ~5K | $0.15–0.30 | Per failure (~15% of issues) |

### 7.2 Scenario Costs

**Single-chunk, 7 issues:**
- Planning (Planner + design review): ~$3–5
- Workers (7 x $0.45 avg): ~$3.15
- Reviews (7 x $0.30 avg): ~$2.10
- Triage (1 failure): ~$0.25
- **Total: ~$9–11**

**Multi-chunk PRD, 3 chunks, 20 issues total:**
- Planning (Architect + 3 Planners + design reviews): ~$15–20
- Workers (20 x $0.45 avg): ~$9
- Reviews (20 x $0.30 avg): ~$6
- Triage (3 failures): ~$0.75
- **Total: ~$31–37**

**Same multi-chunk with Agent Teams (M4b):**
- Planning adds ~$10–15 (all Planners on Opus within Agent Teams)
- **Total: ~$41–52**

### 7.3 Model Selection Strategy

- **Opus:** Planning agents (Mayor, Architect, Planner). These sessions require deep reasoning about code architecture, decomposition, and cross-cutting concerns. Planning is infrequent and high-leverage — a bad plan wastes far more than the cost difference.
- **Sonnet (default for workers):** Workers execute well-scoped issues with detailed instructions, specific files, and test commands. The Planner + design review process ensures issues are clear enough for Sonnet to handle. This is where the cost savings matter — workers are 80%+ of sessions.
- **Opus escalation for workers:** If a worker blocks on the same issue twice (triage_count >= 2), the daemon can optionally respawn with Opus before going to human-blocked. Configurable: `opus_escalation = true` in config.
- **GLM 4.7 / cheaper models (future):** Evaluate after v1 if Sonnet worker costs remain high. Well-scoped issues with TDD enforcement may be tractable for cheaper models. Out of scope for v1.

### 7.4 Cost Controls

- Parallelism limits (default 5 global, 2 per repo) bound concurrent spend
- Adaptive polling reduces API calls during idle periods
- Design review catches bad plans before they generate expensive worker runs
- Issue sizing rules (<=8 files, <=200 lines) keep individual worker sessions short

---

## 8. Milestones

### M1: Daemon skeleton + Linear integration

**Goal:** `dispatchd` runs, polls Linear, manages basic worker spawn/monitor loop.

- `dispatchd` binary: starts, reads config, opens socket
- Linear client: query ready issues, transition states, read/write custom fields
- Worker spawn: git worktree add, tmux session create, claude process start
- Worker monitor: session liveness check, clean exit detection
- `dispatch status` CLI command working

**Exit criteria:** Create a Linear issue manually. Run `dispatchd`. Issue transitions to In Progress, tmux session appears with Claude Code running in a worktree. Issue closes when Claude finishes.

### M2: Failure handling + triage + review gates

**Goal:** Unclean exits and worker-initiated blocks handled correctly. Post-worker review pipeline working.

- Unclean exit detection and Linear transition to Triage
- Triage agent spawn with correct context (on Sonnet)
- `dispatch block` and `dispatch investigate` CLI commands
- Notification emission over socket
- Reopen flow: Blocked → Todo → fresh worker
- Triage circuit breaker (triage_count >= 3 → Blocked)
- Test gate: daemon runs test_cmd before merging to integration branch
- Review agent: Sonnet-based diff review on `dispatch close`, reject on critical issues
- Superpowers plugin installation in worker sessions (TDD, verification, debugging skills)

**Exit criteria:** `kill -9` a running worker. Triage agent spawns, either recovers or flags in Linear. `dispatch investigate` works on a blocked issue. Worker that calls `dispatch close` with failing tests gets rejected and must fix before re-closing.

### M3: Mayor + standalone Planner + design review

**Goal:** End-to-end workflow for single-chunk work with plan approval gate.

- Mayor CLAUDE.md: reads Linear state, routes to Planner, handles blocked issues
- Planner CLAUDE.md: reads repo, writes plan doc, triggers design review
- Oppositional design review: simplicity + completeness subagents (on Sonnet)
- User approval flow: review feedback presented, user approves/edits before issues created
- `dispatch mayor` and `dispatch plan --chunk` CLI commands
- Integration branch creation per chunk (`dispatch/chunk/<name>`)
- Merge of issue branches into integration branch on completion
- Parallelism limits and dependency-aware scheduling in daemon
- Auto-PR creation on chunk completion (default on, `auto_pr = false` to disable)
- `dispatch pr <chunk-name>` for manual PR creation
- `dispatch notify` for immediate daemon polling after Linear writes

**Exit criteria:** Describe a focused task to Mayor → Planner writes plan doc → design review produces simplicity/completeness critiques → user approves → issues created → workers execute with Sonnet + superpowers → review gates pass → integration branch merged → draft PR created automatically.

### M4a: Architect + Sequential Planners

**Goal:** Multi-chunk planning from PRD using sequential standalone Planners (no Agent Teams dependency).

- Architect CLAUDE.md: reads PRD, decomposes chunks, writes interface spec, spawns Planners sequentially
- `dispatch plan --prd` CLI command
- `dispatch escalate` CLI command (Planner → Architect promotion)
- Coherence review pass in Architect after all Planners finish
- Cross-chunk blocking relationship wiring
- Integration branch creation per chunk

**Exit criteria:** Hand a PRD to Mayor → Architect decomposes into chunks → sequential Planners write issues → coherence review passes → workers execute in dependency order across integration branches.

### M4b: Agent Teams upgrade (optional)

**Goal:** Upgrade Architect to use Agent Teams for parallel Planner coordination.

- Architect CLAUDE.md: Agent Teams variant with one Planner teammate per chunk
- Planner teammate CLAUDE.md: peer-to-peer coordination variant
- Incremental commit to Linear (chunk-by-chunk) for crash resilience
- Feature-flagged: `dispatch plan --prd --parallel` or config toggle

**Exit criteria:** Same as M4a but Planners run in parallel and resolve interface conflicts via direct messaging. Measurable reduction in planning time vs. sequential.

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

## 9. Risks and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Agent Teams experimental limitations (no session resumption, all-Opus cost) | High | Medium | Architect commits issues chunk-by-chunk. Agent Teams used only for short planning sessions. Accepted cost for coherence benefit. |
| Agent Teams deprecated or changed before Dispatch ships | Low | Medium | Architect designed so Planner coordination could fall back to sequential standalone Planners with a shared interface doc if Agent Teams is unavailable. |
| Linear API rate limits | Low | Medium | 5s poll interval keeps request rate low. Cache last state snapshot in daemon. |
| Linear API downtime | Low | High | Daemon pauses spawning on API error. Running workers finish. Retry with backoff. |
| Git worktree conflicts | Low | Low | Daemon uses issue-id-scoped branch names: `dispatch/<issue-id>`. Collisions essentially impossible. |
| Triage agent makes things worse | Medium | Medium | Constrained to assessment + minimal recovery only. CLAUDE.md explicitly prohibits large-scale fixes. Circuit breaker: after 3 triage cycles, issue goes to Blocked unconditionally. |
| Worker overshoots issue scope | High | Low | Planner sizing rule (<=8 files, <=200 lines). Triage detects oversized commits and flags. |
| Merge conflicts on integration branch | Medium | Medium | Issues within a chunk are dependency-ordered, so sequential merges minimize conflicts. Conflicts trigger triage. Cross-chunk conflicts are rarer since chunks are independent workstreams. |
| Planner misroutes scope (single vs. multi-chunk) | Medium | Low | Planner can escalate to Architect mid-session via `dispatch escalate`. Cost of one aborted Planner session is low. |
| Review gate adds latency to worker pipeline | Medium | Low | Review agent runs on Sonnet with small diffs (~$0.30, <60s). Test gate runs repo test suite which varies by project. Both run only on close, not continuously. `test_cmd_fast` option for repos with slow suites. |
| Design review slows down planning | Low | Low | Review subagents run concurrently on Sonnet (~$0.30 total). Human approval is the bottleneck by design — catching a bad plan is worth minutes of review. |
| Runaway worker burns tokens | Medium | Medium | 30-minute session timeout (configurable). Max 3 close attempts before force-block. Issue sizing rules keep tasks small. Opus escalation is a single retry, not open-ended. |
| Concurrent workers conflict on merge | Medium | Low | Per-chunk merge queue serializes integration merges. Planner flags overlapping files. Daemon prefers serial execution for issues sharing file paths. Conflicts fall through to triage. |
| Plan approval left unattended | Low | Low | Planner waits indefinitely (correct — approval is human decision). Daemon emits reminder notification after 1 hour. No token burn while waiting. |

---

## 10. Success Metrics

After 2 weeks of daily use:

- **Time from idea to first worker executing:** <5 minutes (single-chunk), <15 minutes (multi-chunk from PRD). Includes plan approval time.
- **Review gate first-pass rate:** >80% of workers pass test gate + AI review on first `dispatch close`
- **Worker clean exit rate:** >85% of issues reach Done without triage
- **Triage auto-recovery rate:** >60% of unclean exits recovered without human intervention
- **Cross-chunk coherence:** no interface mismatches discovered during worker execution for Architect-planned work
- **Design review value:** >50% of plans incorporate at least one piece of reviewer feedback before approval
- **Daemon uptime between intentional restarts:** >48 hours
- **TUI open/close cycle:** <1 second, zero effect on running workers
- **Cost per issue (worker + review):** <$1.00 average on Sonnet

---

## 11. Non-Goals and Future Considerations

**Explicitly out of scope for v1:**
- Multi-user support
- CI/CD integration (workers run tests locally; CI is separate)
- Windows support (macOS + Linux only; development may happen on Windows via WSL but Windows is not a supported target)
- GUI of any kind
- Persistent agent identity
- Per-chunk model selection in Agent Teams (not yet supported)
- Alternative worker models beyond Sonnet (e.g., GLM 4.7 — explore later if Sonnet costs are prohibitive)

**Things to watch:**
- Agent Teams stability and session resumption — adopt improvements as they land
- Per-role model selection in Agent Teams (community feature request) — when available, run Planners on Sonnet to reduce planning cost significantly
- Claude Code orchestration features generally — adopt anything that simplifies Architect or Worker design
- GLM 4.7 and other cost-effective models for worker tasks — if well-scoped issues prove tractable for cheaper models, adopt to reduce operational cost

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
