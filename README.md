# Dispatch

A capture-first task ledger and agent orchestrator for one developer's machine. Two binaries (`dt`, `dispatchd`), one SQLite database, a few prompt files. herdr is the terminal substrate; Claude Code (and optionally Codex) are the agents.

The problem it solves: a thought arrives — *"I want a viewer for this data"* — and the usual response is `cd somewhere && claude`, after which the intent is never recorded anywhere and started work quietly dies. Dispatch makes capture cost one command, puts everything in flight on one board, and keeps a searchable record of what was decided and declined.

Design history: `dispatch-prd_1.md` (the v1 tracker + daemon), `dispatch-prd_2.md` (the capture-first layer).

## Quick start

```bash
go build -o ~/bin/dt ./cmd/dt
go build -o ~/bin/dispatchd ./cmd/dispatchd
herdr integration install claude    # accurate agent lifecycle states

cd ~/src/some-repo
dt go "i want a viewer for this data"   # thought → running agent, ~2s
dt tui                                   # the board (run it in its own herdr tab)
```

State lives in `~/.dispatch/dispatch.db` (override: `--db` or `DISPATCH_DB`).

## The intended workflow

### 1. Capture — `dt go "<thought>"`

One command, thought to running agent:

1. Infers the repo from your cwd (`-r <path>` to override).
2. Checks the closed ledger for similar past work (see Dedup below); if a judge call confirms a match, you see it — with why it was killed — and confirm with `y` or walk away.
3. Inserts a `live` task. The **thought is stored verbatim and is immutable**; a display label starts as a truncation and is replaced by a ≤3-word generated one after the pane is focused.
4. Creates a git worktree at `~/.dispatch/wt/<id>` on branch `dispatch/<id>` (`--here` skips this and runs in your current tree).
5. Opens a herdr tab in the repo's workspace (workspace = repo, tab = task, pane = agent), launches `claude` with your thought as the opening message, and focuses you into it.

The session is launched with an injected dispatch contract (`internal/agentctx/session.md`), so it knows its task ID, how to file proposals, and what the unattended protocol expects — regardless of the user's global agent config.

`dt adopt "<why>"` registers a session you already started by hand in a herdr pane.

### 2. Work, then disposition

Interactive work pays no ceremony. When you're done with a live task, it goes one of four ways:

| You want to... | Command | What happens |
|---|---|---|
| It's finished | `dt done <id>` | Marked done. Merging is yours — live work never auto-merges. |
| Abandon it | `dt kill <id> "<reason>"` | Reason is **mandatory** and becomes part of the searchable record (it's what dedup shows you next time). Tab closed, worktree and branch removed. |
| Shelve it | `dt park <id>` | Tab closed, worktree kept. `dt resume <id>` reopens the tab and continues the conversation (`claude --continue`). |
| Walk away and let it finish | `dt promote <id>` | See below — the one transition that costs ceremony. |

### 3. Walk away — `dt promote`

`live → unattended` is the only gated transition on the human path. It requires a written acceptance condition, because that's what the daemon gates the merge on:

```bash
dt promote 4e9a -k ratchet -a "go test ./..."                   # command that must exit 0
dt promote 4e9a -k report  -a "the viewer renders sample.json"  # prose, judged by a reviewer agent
```

Run without flags in a terminal and it prompts. In-place (`--here`) tasks can never be promoted — no branch to merge, and a dirty tree must never auto-merge.

With `dispatchd` running, a watcher blocks on the herdr socket (`herdr agent wait` — event-driven, no polling, no tokens) until the task's agent settles. Then the **real** verification runs — never herdr's `done` badge (invariant I3):

- **ratchet**: the acceptance command runs in the workdir; exit 0 passes.
- **report**: a read-only reviewer agent judges the written condition against *committed* work and must end its output with `VERDICT: approve` or `VERDICT: reject — <reason>`. No verdict = blocked for human triage, never an implicit approve.

Approve → branch merges into the base branch, task done, tab closed, worktree removed. Reject → the reason is prompted back into the *same interactive session* (which still has all its context), and the agent gets another round. At **3 rejections** the task blocks with the accumulated reasons — the overnight token-burn cap. A pane lost to a herdr restart is respawned with `claude --continue`.

### 4. Proposals — how agents file work

`dt add` is the agent-facing intake. Tasks it creates start as **`proposed`** and are never auto-dispatched: they surface in the board's Needs You lane and wait for `dt reopen <id>` (approve) or `dt kill` (decline). This is the guardrail that lets every agent on the machine know about `dt add` while a daemon runs unattended.

Approved (`open`) tasks enter the v1 queue: `dispatchd` picks ready ones (unclaimed, dependencies met), spawns a worker agent in a fresh worktree with `prompts/worker.md`, runs the review gate on completion, merges approved work, and auto-completes parents whose children all finish. Task descriptions are the whole contract here — the worker only ever sees `dt show <id>`.

### 5. The board — `dt tui`

Run it in its own herdr tab and leave it. SQLite is polled on a 2s tick; herdr agent state is read live. Lane order encodes urgency, not lifecycle:

1. **Stale · resume or kill** — live tasks whose agent is idle/absent with no workdir commits in `DISPATCH_STALE_DAYS` (default 4). Derived at read time; nothing stored, nothing to drift. This lane is the point of the product: dead work doesn't announce itself.
2. **Needs you** — blocked tasks, agents waiting on input, and proposals awaiting your call.
3. **Live now** — with live agent state; `done ✓ awaiting your call` means the agent stopped talking, not that the work is right.
4. **Unattended** — the daemon's.
5. **Parked / Closed this week** — collapsed to counts (`z` expands).

Rows show the label; **the focused row reveals the verbatim thought**. The label is a display cache and never authoritative — retrieval and dedup always run on the thought.

| Key | Action |
|---|---|
| `j`/`k` | Move |
| `Enter` | Focus the task's herdr tab (the board stays alive; revives a dead tab via `dt resume`) |
| `g` | Capture — inline `dt go` |
| `u` | Promote — `report <condition>` or `ratchet <command>` |
| `x` / `p` / `r` | Kill (reason required) / park / resume |
| `:` | Fuzzy command — English in, a `dt batch` you confirm out |
| `b` | Brief — what changed since you last looked |
| `z` / `q` | Expand collapsed lanes / quit |

Mutations shell out to `dt` itself — the TUI never writes SQLite.

### Dedup at capture

Two stages, so the common case costs nothing. Stage 1 is deterministic and local: token-overlap scoring over the closed ledger (`done`/`killed`/`parked`), matching the verbatim thought plus kill reason — never the label. Stage 2 runs only on hits: one model call judges the top 5 ("same work, not merely related?"). Matches render with their closure reasons; `y` starts anyway; `--no-dedup` skips the check.

## Command reference

### v2 (capture-first)

| Command | Purpose |
|---|---|
| `dt go "<thought>" [--here] [-r <repo>] [--no-dedup]` | The capture path: thought to running agent |
| `dt adopt "<why>"` | Register the current herdr pane as a task |
| `dt kill <id> <reason>` | Close without completing; reason mandatory; tears down tab + worktree |
| `dt park <id>` / `dt resume <id>` | Shelve / bring back (also revives live tasks with dead tabs) |
| `dt promote <id> [-k report\|ratchet] [-a "<cond>"]` | Hand a live task to the daemon |
| `dt relabel <id> "<text>"` | Fix a bad display label (syncs the herdr tab) |
| `dt tui` | The board |

### v1 (ledger + queue)

| Command | Purpose |
|---|---|
| `dt add <title> [-d <desc>] [-p <parent>] [--after <id>] [-r <repo>]` | File a task — starts `proposed` |
| `dt reopen <id>` | Approve a proposal (or reopen blocked/done work) |
| `dt dep` / `undep` / `claim` / `release` / `done` / `block` / `note` / `edit` | Ledger operations |
| `dt ready` / `dt list [--tree\|--all\|--status s]` / `dt show <id>` | Read state (all support `--json`) |
| `dt batch` | Execute many commands atomically from stdin (includes v2 verbs kill/park/resume/relabel) |
| `dt init <repo-path>` | Register a repo in `~/.dispatch/config.toml` |

### dispatchd

```bash
dispatchd --worker-prompt prompts/worker.md --reviewer-prompt prompts/reviewer.md \
          [--worker-agent claude|codex] [--reviewer-agent claude|codex] \
          [--repo <path>] [--base-branch <b>] [--poll-interval 5s] [--gp]
```

One daemon runs both loops: the v1 queue (poll every 5s, spawn workers in worktrees, review gate, merge, parent auto-complete) and the unattended watchers (event-driven on the herdr socket). Without herdr on the machine, unattended dispatch disables itself and the v1 loop is unaffected.

Workers and reviewers run non-interactively with permissions bypassed — the worktree is the isolation boundary and enforcement lives in the daemon, not in agent gates. `--worker-agent codex` / `--reviewer-agent codex` mix agents freely (e.g. claude writes, codex gives an independent second-opinion review).

`--gp` enables GraphPilot sync: `gp sync-child <task-id>` on completion, and `dt batch` auto-wires the graph when `GRAPHPILOT_NODE` is set.

## Model calls

Every model call is one-shot and stateless — no resident process, ever (a prior system burned 62M tokens polling a TUI; the invariants in `dispatch-prd_2.md` §5 exist because of it). All calls go through `internal/llm` (`claude -p --model haiku` by default).

| # | Site | Trigger | Contract |
|---|---|---|---|
| 1 | Label | after `dt go` focuses the pane | ≤3 words; any failure silently keeps the truncation |
| 2 | Dedup judge | capture, only when retrieval finds candidates | JSON `[{id, reason}]`; prose is a hard error |
| 3 | Fuzzy command | `:` in the TUI | JSON array of `dt batch` lines, rendered for confirmation |
| 4 | Brief | `b` in the TUI | prose ≤200 words |
| 5 | Acceptance reviewer | unattended agent settles | `VERDICT: approve` / `VERDICT: reject — reason` |

For sites 2, 3, and 5 the output contract is strict by design: the single permitted normalization is a markdown fence wrapping pure JSON; anything else is a hard error shown to the human. Lenient parsing is how a tool erodes into a coordinator.

## Configuration

| Knob | Default | Meaning |
|---|---|---|
| `DISPATCH_DB` / `--db` | `~/.dispatch/dispatch.db` | Database path |
| `DISPATCH_STALE_DAYS` | 4 | Stale-lane threshold |
| `DISPATCH_LLM_BIN` / `DISPATCH_LLM_MODEL` | `claude` / `haiku` | One-shot model calls |
| `DISPATCH_WORKER_AGENT` / `DISPATCH_REVIEWER_AGENT` | `claude` | Daemon agent CLIs |
| `DISPATCH_BASE_BRANCH` / `--base-branch` | auto-detect | Merge target |
| `~/.dispatch/config.toml` | — | Per-repo settings (`max_workers`) |

## Architecture

```
dispatch-ai/
  cmd/dt/               CLI: capture, lifecycle, ledger, batch, tui entry
  cmd/dispatchd/        Daemon entry point
  internal/
    db/                 SQLite: tasks (v1+v2 columns), deps, notes, meta; migrations
    daemon/             v1 queue workers + review gate; unattended watchers;
                        CLISpawner (claude/codex) + RoleSpawner; worktree/merge ops
    mux/                Thin interface over the herdr CLI (pre-1.0; the swap point)
    tui/                The board: lanes, staleness, fuzzy command, brief
    llm/                The single model chokepoint (one-shot, 30s timeout)
    dedup/              Capture-time retrieval + judge contract
    agentctx/           Session contract injected into every captured session
    config/, id/        Config file parsing; 4-char hex IDs
  prompts/              worker.md, reviewer.md (v1 daemon system prompts)
```

**Sources of truth** (invariant I5): SQLite owns intent and status; herdr owns liveness; git owns activity. Nothing duplicates — staleness and dedup are derived at read time.

**herdr state is an attention signal, never a correctness gate** (I3): `done` means the agent stopped talking. Merge decisions use git, the ratchet command, and the reviewer.

**Enforcement lives in Go** (I6): the review gate, the reject cap, the proposed default, and the promote gate are code, not prompt text.
