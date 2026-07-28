# Dispatch PRD 2 — Capture-First

Status: draft
Layers on top of the existing `dt` / `dispatchd` / SQLite core. Substrate: **herdr**, not tmux.

---

## 1. Problem

A thought arrives. *Let's run these experiments. I want a viewer for this data. I want to factor this fix out into its own PR.* The current response is to open Claude Code in whatever directory the terminal happens to be in, and start.

Two things go wrong from there:

1. **The intent is never recorded anywhere.** It exists only in the first message of a session transcript. Three days later the worktree is still there and the reason it exists is gone.
2. **Started work quietly dies.** Five sessions get started; two finish. The other three aren't *blocked* — nothing is asking about them. They're just not on any surface, so they don't come back.

Agent view shows sessions. It does not show what any of them was *for*, does not persist across restarts, and has no notion of a session having been abandoned.

The failure mode is not orchestration. It's **capture and abandonment**.

## 2. What this is

A product-manager layer over work I'm already doing: durable intent at the moment of the thought, one visual surface for everything in flight, and a searchable record of what was decided and declined.

The existing enforcement machinery — worktrees, review gate, atomic batch, dependency graph — stays as built. It becomes the path work takes *when I want to walk away from it*, rather than the default path everything must go through.

## 3. Non-goals

- **Not an autonomous orchestrator.** No resident model process. No LLM coordinator supervising workers.
- **Not a terminal replacement.** herdr is the runtime; Claude Code is the agent. This layer gets me to the right pane and remembers why I opened it.
- **Not a planning tool.** GraphPilot owns the shape of work across weeks. This owns a slice of it in flight.
- **Not multi-machine.** Single laptop, one SQLite file. (herdr `--remote` may make this interesting later; out of scope for now.)
- **Not a backlog.** There is deliberately no "ready" queue for hand-captured work. Anything captured is either started or it isn't a task.

## 4. Design principles

**Capture must be free.** Any feature that adds a keystroke to the capture path is suspect by default. The competition is not another tool — it's `cd somewhere && claude`, which costs nothing. If capture costs more than that, it won't get used, and everything downstream is moot.

**The ledger entry is a byproduct of starting.** Not a prerequisite. Nothing is *filed*; work is started in a place the board happens to know about.

**Ceremony is a tax on walking away.** Acceptance criteria, reviewer gates, and auto-merge apply only to work I want to run unattended. Interactive work pays none of it.

**Verbatim is stored; a label is displayed.** The sentence I typed is immutable and searchable forever. A generated three-word label is what makes the board scannable. The label is a display cache and is never authoritative — the focused row always reveals the original words.

**Abandonment is the primary failure to surface.** Blocked work announces itself. Dead work doesn't. The board's top lane is the stuff quietly rotting.

**The model prepares decisions; it does not make them.** Taste doesn't transfer. What transfers is retrieval, translation, and summarization.

## 5. Invariants

These exist because a prior system burned 62M input tokens polling a TUI.

| # | Invariant |
|---|---|
| I1 | No model process is resident. Every model call is one-shot, triggered by a keystroke or a state transition. |
| I2 | **No model ever reads pane text.** herdr may scrape terminal output — that's deterministic Rust at zero token cost, which is the entire point of using it. The prohibition is on the LLM doing it. |
| I3 | **herdr agent state is an attention signal, never a correctness gate.** `done` means the agent stopped talking, not that the work is right. Merge decisions use git, tests, and the reviewer — never a state badge. |
| I4 | The model's only write path is emitting `dt batch` argv, rendered for confirmation. |
| I5 | SQLite is the source of truth for intent and status. herdr is the source of truth for liveness. Neither duplicates the other. |
| I6 | Enforcement lives in Go. Prompt text is never the mechanism by which a rule holds. |

## 6. Why herdr

Switching substrate deletes three subsystems that would otherwise need building:

| Need | tmux | herdr |
|---|---|---|
| "Which worker is waiting on me?" | Hooks → named pipes → `tail -F` → inbox renderer | Native `blocked` state, zero config |
| "Is this worker alive / done?" | Process-exit detection, 5s poll | `herdr wait agent-status <pane> --status done` — blocking, event-driven |
| "Show me everything at once" | N detached sessions, one attach each | One persistent server, panes in one view |
| Reading a pane programmatically | `capture-pane` + regex | `herdr pane read <id> --source recent-unwrapped` |

The `blocked` state is the big one. The entire question-inbox design from the earlier coordinator — hooks writing to named pipes, a `tail -F` event loop, a batching renderer — collapses into a field herdr already computes. Claude Code has a native integration (`herdr integration install claude`) that reports lifecycle directly rather than by inference, which is the accurate path.

`herdr wait agent-status` is the second: it removes `dispatchd`'s 5s poll entirely. Block on a socket rather than spinning.

## 7. herdr topology

**Workspace = repo. Tab = task. Pane = agent.**

```
herdr server
├── workspace "sc-api"        (--cwd ~/src/sc-api)
│   ├── tab "data viewer"     ← task 4e9a, pane cwd = worktree
│   │   └── pane 3-1  claude
│   ├── tab "retry fix PR"    ← task 7c02, in-place
│   │   └── pane 4-1  claude
│   └── tab "board"           ← dt tui lives here
└── workspace "sdk"
    └── tab "bump sqlite3"    ← task 2b44
```

A task is a **tab**, not a session. Multiple tasks in one repo are siblings in one workspace, visible in one herdr sidebar, switchable without detach/attach. The tab label is the generated three-word label, so herdr's own tab bar becomes readable for free.

Extra panes within a task's tab (test watcher, log tail) are available via `herdr pane split` without restructuring anything.

**Attach is focus, not handoff.** When `dt tui` runs inside a herdr pane (`HERDR_ENV=1`), pressing Enter sends a focus command over the socket and the board stays alive in its own pane. `ctrl+b` gets back. No terminal suspend, no `tea.ExecProcess`. Outside herdr, fall back to `tea.ExecProcess` + `herdr attach`.

**Consequence: the hold problem mostly evaporates.** With no terminal handoff, and `live` tasks never touched by the daemon, `held_by` is only needed for the rare case of intervening in an `unattended` task. It stays in the schema but drops out of the hot path.

## 8. State model

Six statuses in SQLite. `stale` is deliberately **not** one — it's derived, so it can't drift out of sync with reality.

| Status | Meaning | Who moves it |
|---|---|---|
| `live` | A tab exists. Human-owned. Never auto-merges. | Human |
| `unattended` | Has acceptance. Daemon owns it. Reviewer gates merge. | Human promotes; daemon runs |
| `blocked` | Needs triage: crash, reviewer rejection, unmet dependency. | Daemon |
| `parked` | Deliberately shelved. Tab closed, worktree kept. | Human |
| `done` | Merged or otherwise complete. | Daemon or human |
| `killed` | Closed without completion. **Reason mandatory.** | Human |
| `proposed` | Agent-discovered. Never auto-dispatches. | Agent writes; human promotes |

**herdr state is a separate axis**, read live from the socket and never persisted:

| herdr | Board effect |
|---|---|
| `blocked` | Row floats into **Needs you** — the agent is waiting on input |
| `working` | Normal live row |
| `idle` / `unknown` | Feeds the staleness clock |
| `done` | Live row flagged: agent stopped, awaiting your call |

**Derived views**

- `stale` — `live` tasks with no commits and herdr `idle`/absent for N days.
- `orphan` — tasks with no `gp_node`, closed in the last week. Drift measurement.

**Transitions**

```
(thought) ──dt go──▶ live ──┬──▶ done
                            ├──▶ killed  (reason required)
                            ├──▶ parked
                            └──▶ unattended  (acceptance required)

unattended ──┬──▶ done      (reviewer approves → merge)
             └──▶ blocked   (crash, or rejection cap hit)
```

`live → unattended` is the only gated transition on the human path. That gate is the entire ceremony budget.

## 9. Schema changes

Additions to `tasks`:

| Column | Type | Notes |
|---|---|---|
| `thought` | TEXT NOT NULL | Verbatim capture. **Immutable.** The searchable record. |
| `label` | TEXT | ~3 words. Generated async, hand-editable, display-only. |
| `mode` | TEXT | `worktree` \| `in_place` |
| `workdir` | TEXT | Absolute path — worktree path or adopted cwd |
| `herdr_ws` | TEXT | Workspace label |
| `herdr_pane` | TEXT | Pane ID, e.g. `3-1` |
| `held_by` / `held_pid` | TEXT / INT | Only for intervening in `unattended` work |
| `acceptance_kind` | TEXT NULL | `report` \| `ratchet` \| NULL |
| `acceptance` | TEXT NULL | Prose, or command + threshold |
| `kill_reason` | TEXT NULL | Required when `killed` |
| `last_activity` | TIMESTAMP | max(last commit in workdir, last herdr non-idle) |
| `reject_count` | INT DEFAULT 0 | Reviewer rejection counter |

```sql
CREATE VIRTUAL TABLE task_fts USING fts5(
  id UNINDEXED, thought, label, kill_reason, acceptance,
  content='tasks', content_rowid='rowid'
);
```

`title` retained for backward compat, no longer displayed when `label` or `thought` is present.

### Label generation

Zero latency on the capture path, by construction:

1. On insert, `label` = first ~4 words of `thought`, truncated. The board is usable immediately.
2. **After** the pane spawns and the human is already typing, fire one async model call: thought → ≤3 words. Update the row and the herdr tab label.
3. On failure, keep the truncation. Never block, never retry aggressively.
4. `dt relabel <id> "<text>"` for when it picks badly.

The label is never used for retrieval or matching — `thought` is. It exists only so the board scans.

## 10. Command surface

### `dt go "<thought>"`

The whole capture path. One command, thought to running agent.

```
dt go "i want to factor the retry fix into its own pr"
dt go --here "..."          # dirty tree, no worktree
dt go -r sdk "..."          # override inferred repo
dt go --no-dedup "..."
```

1. Infer repo from cwd (or the `HERDR_ENV` workspace); prompt if ambiguous.
2. Dedup retrieval (§12). If candidates clear threshold, show and require `y`.
3. Insert task: `live`, `thought` verbatim, `label` = truncation.
4. Unless `--here`: `git worktree add ~/.dispatch/wt/<id>`.
5. `herdr tab create --label "<label>"` in the repo's workspace; `herdr pane run <pane> "claude"` with the thought as the opening message; store `herdr_pane`.
6. Focus the pane.
7. Fire the async relabel.

### Others

| Command | Purpose |
|---|---|
| `dt adopt` | Register a session already running somewhere. Detects herdr pane from `HERDR_ENV`. Prompts for the one-line why. |
| `dt kill <id>` | Reason mandatory. Closes tab, tears down worktree. |
| `dt park <id>` / `dt resume <id>` | Shelve without killing; worktree preserved. |
| `dt promote <id>` | `live → unattended`. Prompts for acceptance kind and condition. Refuses without one. |
| `dt relabel <id> "<text>"` | Fix a bad label. |
| `dt tui` | The board (§11). |
| `dt review` | Weekly PM digest over the *closed* ledger. A markdown document, not a dashboard — different cadence from the board on purpose. Includes orphan count. |

Existing commands unchanged. `dt add` remains the agent-facing path and now defaults to `proposed`.

## 11. TUI specification

Bubble Tea, running in its own herdr tab.

**Lane order encodes urgency, not lifecycle:**

1. **Stale · resume or kill** — derived
2. **Needs you** — status `blocked`, or herdr state `blocked`
3. **Live now** — `live`
4. **Unattended** — `unattended`
5. **Parked** *(collapsed, count only)*
6. **Closed this week** *(collapsed, count only)*

**Rows show the label. The focused row reveals the verbatim thought.**

```
 Stale · resume or kill (2)

▸ 4e9a  data viewer                  idle 6d
        "i want a viewer for this data"
        sc-api · worktree · no commits

  d117  tokenizer double-encode      idle 11d

 Needs you (1)

  7c02  factor out retry fix         blocked
        rejected: touches 2 unrelated files

 Live now (2)

  b81d  ablation sweep               working
  9f30  jwt refresh race             done ✓
```

**Keys**

| Key | Action |
|---|---|
| `j`/`k` | Move (flat index across lanes) |
| `⏎` | Focus that task's herdr pane |
| `g` | Capture (inline `dt go`) |
| `a` `k` `p` `u` `r` | Adopt · Kill · Park · Promote · Relabel |
| `:` | Fuzzy command |
| `b` | Brief — what changed since last look |
| `q` | Quit |

herdr state is read live over a socket subscription, not polled. SQLite is polled on a 2s tick — free, local, no model involved.

## 12. Dedup at capture

The "without repeating" mechanism. Runs at `dt go`, not at promotion — capture is when a duplicate is cheapest to catch.

Two stages, so the common case costs nothing:

1. **FTS5** over `thought`, `kill_reason`, `acceptance` across `done`, `killed`, `parked`. Local, sub-millisecond, offline. No candidate over the score floor ⇒ proceed silently.
2. **One model call** judging the top ~5: are any of these the same work? Returns `[{id, reason}]` or empty.

```
Similar closed work

  c5e8  "clean up session leak on merge failure"
        killed 12 May: fixed upstream in go-git 5.11

  Still start? y / n / v view diff
```

`y` is one keystroke, so false positives are nearly free. Budget: err toward showing.

On completion of research-shaped work, the *decision* must land in the repo's `DECISIONS.md` or `CLAUDE.md`, with `dt note` pointing at the commit. Decisions stored only in the ledger are invisible to the agents that need them.

## 13. LLM call sites

Five. All one-shot, all stateless.

| # | Site | Trigger | Output contract |
|---|---|---|---|
| 1 | Label | Async, post-spawn | ≤3 words, plain text |
| 2 | Dedup judge | Capture, conditional on FTS hits | `[{id, reason}]` |
| 3 | Fuzzy command | `:` keystroke | JSON array of `dt` argv |
| 4 | Brief | `b` keystroke | Prose, ≤200 words |
| 5 | Reviewer | Unattended completion *(exists)* | Approve/reject + reason |

For 2–4: output that doesn't match the contract is a **hard error** shown to the human. No salvage parsing, no regex extraction. The moment parsing becomes lenient, the boundary erodes and this becomes a coordinator. Site 1 is exempt — it's cosmetic, and its failure mode is keeping the truncation.

Site 3 always renders for confirmation and executes via `dt batch` on stdin, atomically.

## 14. `dispatchd` changes

**Spawn into herdr.** `herdr tab create` + `herdr pane run` rather than a bare exec, behind a thin `internal/mux` interface (see risks).

**Replace the 5s poll.** Per unattended task, block on `herdr wait agent-status <pane> --status done`. Event-driven, zero cost. Subscribe to state changes for the board.

**Never trust `done` as a gate (I3).** On herdr `done`, run the actual verification — tests, then reviewer against the written acceptance condition. The badge only says it's worth looking.

**Only `unattended` tasks are dispatched.** `live` tabs are human-owned; the daemon never spawns into them or merges them.

**Rejection cap.** Reject → reopen → redo → reject is unbounded and burns tokens overnight. At `reject_count >= 3`, go to `blocked` with the accumulated reasons.

**Staleness scan.** Hourly. `last_activity` = max(last commit in workdir, last herdr non-idle timestamp).

**Hold liveness.** If `held_pid` isn't alive, clear the hold and log it. Ship `dt release-hold <id>` regardless.

## 15. Milestones

**M0 — Capture and board.** Schema migration; `dt go`, `dt adopt`, `dt kill`, `dt park`; herdr topology (workspace/tab/pane) and focus; `dt tui` with all lanes, label rows, verbatim-on-focus, live herdr state. Labels are truncation-only — **no model calls anywhere in M0.** This is the whole product for someone who never wants automation, and it's the version to live on for a week before building anything else.

**M1 — Abandonment.** Staleness scan, stale lane, resume semantics, collapsed closed lane.

**M2 — Labels.** First model call site. Async generation, `dt relabel`, herdr tab label sync.

**M3 — Dedup.** FTS5 table, backfill, capture-time retrieval, judge call.

**M4 — Assistance.** Fuzzy command box, brief, `last_seen` tracking.

**M5 — Walk-away.** `dt promote`, acceptance kinds, `herdr wait` dispatch loop, rejection cap, reviewer gated on written acceptance.

**M6 — PM cadence.** `dt review` digest, orphan detection against GraphPilot.

The recursive test applies from M0: build M1 onward using M0.

## 16. Risks

| Risk | Mitigation |
|---|---|
| **Capture friction creep** — prompts accumulate until `dt go` is slower than `cd && claude`. | Track capture latency as a budget. Every new prompt on that path needs explicit justification. |
| **herdr is v0.7.1, pre-1.0.** API churn is likely. | Thin `internal/mux` interface: `CreateTab`, `RunPane`, `Focus`, `AgentState`, `WaitStatus`. Keep a tmux implementation as the escape hatch — the one thing worth borrowing from firstmate's backend abstraction. |
| **herdr `done` is heuristic** — a plausible summary can read as completion. | I3. Never gate a merge on it. |
| **AGPL-3.0-or-later.** | Dispatch shells out to a binary and a socket rather than linking, which is normally outside copyleft scope — but worth confirming before open-sourcing, and commercial licenses exist. Not legal advice. |
| Label drift — a bad 3-word label makes a task unfindable | Retrieval always runs on `thought`, never `label`. `dt relabel` for display. |
| Board becomes a graveyard | Closed and parked lanes collapsed to counts by default |
| Reviewer thrash | Rejection cap → blocked |
| `--here` corrupting a dirty tree | Never auto-merges, cannot be promoted |

## 17. Open questions

1. **Does `herdr tab create` accept `--cwd`?** If not, worktree tasks need `herdr pane run <id> "cd <wt> && claude"`. Verify against v0.7.1.
2. **Is there a `pane focus` in the socket API?** The TUI supports click-to-focus; confirm it's exposed. If not, M0's Enter key falls back to `tea.ExecProcess`.
3. **Does the Claude Code integration report lifecycle natively, or infer from output?** Changes how hard I3 needs to bite.
4. **Staleness threshold** — fixed N days, per-repo, or adaptive to observed cycle time?
5. **What does `done` mean for `--here` work?** No branch to merge. Close, or require a commit reference?
6. **Kill vs. done for research** that produced an answer but no code. May need a `resolved` terminal state.
7. **Is GraphPilot orphan detection v1?** The most interesting number the system could produce, but it depends on `gp_node` discipline that may not exist yet.
