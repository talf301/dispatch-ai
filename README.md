# Dispatch Agent Team

A lightweight Agent Teams setup for interactive planning → autonomous execution workflows.

## Architecture

```
You (human)
  ↕ interact directly
Dispatch (thin coordinator, stays in context)
  ├── spawns → Planner (deep interactive planning, disposable)
  │                ↕ you interact via tmux pane
  │                → writes Tasks with dependencies
  └── spawns → Workers (small pool, claim from task queue)
                   → claim task, implement, mark done, claim next
                   → isolated worktrees, loop until queue empty
```

**Dispatch** stays light. It orients you, routes work, and tracks status. It never reads code or does deep thinking.

**Planner** is where you spend focused time. It uses Superpowers brainstorming and writing-plans skills to produce a rigorous spec and plan, then converts that plan into Tasks. When done, the planner session can end — its context was heavy but that's fine.

**Workers** are a small pool (2-4) that self-claim from the task queue. Each runs in an isolated worktree, implements with TDD, self-reviews, marks done, then claims the next ready task. Workers loop until no tasks remain. If a worker's context gets heavy after many tasks, dispatch can replace it with a fresh one.

## Prerequisites

- Claude Code with Agent Teams enabled:
  ```json
  // settings.json
  { "env": { "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS": "1" } }
  ```
- tmux (for split pane teammates)
- Superpowers plugin installed (for planner skills)

## Setup

Copy the agent definitions to your project or user directory:

```bash
# Project-level (shared with team)
cp -r .claude/agents/ /path/to/your/project/.claude/agents/

# Or user-level (personal, all projects)
cp .claude/agents/*.md ~/.claude/agents/
```

## Usage

### Start a session

```bash
export CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS="1"

# Start dispatch — it's not project-specific
claude --agent dispatch
```

Dispatch reads `~/.dispatch/state.md` (global, tracks all projects) and checks task lists for each registered project.

### Plan a feature

Tell the lead: "I want to plan a new feature for acubemy"

Dispatch spawns a planner teammate scoped to the acubemy directory with the right task list ID. Switch to its tmux pane (Shift+Down) and interact directly. Brainstorm, iterate, approve the plan. The planner writes Tasks.

### Execute

Tell the lead: "Tasks are ready for acubemy, start workers"

Dispatch spawns 2-4 worker teammates scoped to acubemy. Workers self-claim ready tasks, implement, and move on. Workers loop until the queue is empty.

### Work across projects

Dispatch tracks all your projects. You can:
- "What's the status across all my projects?"
- "Spawn a planner for the RelationalAI project"
- "Start workers for both acubemy and RelationalAI"

Each project gets its own task list ID and teammates scoped to its directory.

### Check in later

```bash
claude --agent dispatch
```

Dispatch reads `~/.dispatch/state.md` and all registered task lists, catches you up.

### Context management

If the lead's context gets heavy after a long session:
1. Ask dispatch to "write the state ledger and wrap up"
2. Start a fresh dispatch session — it picks up from the ledger + task list

Planners and workers are naturally disposable — they do their job and end.

## Hooks

A `SessionStart` hook is included that reads `~/.dispatch/state.md` on fresh session start and injects it as context. This ensures dispatch always picks up where the last session left off without relying on the agent to remember to read the file.

Add to your user-level settings (`~/.claude/settings.json`):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "bash ~/.claude/hooks/session-start.sh"
          }
        ]
      }
    ]
  }
}
```

The hook output is injected as additional context at session start. If no state ledger exists yet, the hook outputs nothing.

## State ledger

Dispatch maintains `.dispatch/state.md` in your project root. This is a plain markdown file summarizing:
- Active task lists and their status
- What's in flight, recently completed, blocked
- Key decisions made during planning

This file is the "memory" that lets a fresh dispatch session pick up where the last one left off. Tasks provide the detailed state; the ledger provides the narrative context.

## Customization

### Adjust models

Edit the `model:` field in each agent's frontmatter:
- Lead: `sonnet` (lightweight coordination)
- Planner: `opus` (deep reasoning for design work)
- Worker: `sonnet` (implementation, cost-effective)

### Add a reviewer agent

Create `.claude/agents/reviewer.md` for post-task review:

```yaml
---
name: reviewer
description: Reviews completed task diffs against specs. Approves or requests changes.
model: sonnet
tools: Read, Grep, Glob, Bash, Task
---
```

### Project-specific worker instructions

Add to your project's `CLAUDE.md`:

```markdown
## Worker instructions
- Run `npm test` after each change
- Follow the style guide in docs/STYLE.md
- Commit messages use conventional commits format
```

Workers inherit CLAUDE.md automatically.
