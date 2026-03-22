# Dispatch Planner Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the dispatch-planner skill and supporting daemon/CLI infrastructure (parent task merge model, batch back-references) so that a spec can be decomposed into parallel `dt` tasks that accumulate completed work into a PR-ready branch.

**Architecture:** Four components: (1) `dt batch` gains back-reference support so the planner can create parent + children + deps atomically, (2) the DB layer excludes parent tasks from ready queries and auto-completes parents when all children finish, (3) the daemon branches children from the parent branch and merges completed work back, (4) the planner skill itself is a superpowers markdown file.

**Tech Stack:** Go, SQLite, git (worktrees + merge), superpowers skill framework

---

### Task 1: `dt batch` back-references

Add `$1`, `$2`, etc. substitution to batch commands so that IDs returned by earlier `add` lines can be referenced in later lines (for `-p`, `dep`, `--after`).

**Files:**
- Modify: `cmd/dt/commands/batch.go`
- Test: `cmd/dt/commands/batch_test.go` (create)

- [ ] **Step 1: Write failing test for back-reference substitution**

Create `cmd/dt/commands/batch_test.go`. Test that batch input like:
```
add "Parent" -d "parent task"
add "Child" -d "child" -p $1
dep $1 $2
```
produces a parent task and a child task with the correct parent_id and dependency wiring.

This requires a test helper that runs batch lines against an in-memory DB. Use `db.Open()` with a temp path, create a `*db.DB`, then call `executeLine` in a transaction. Since `executeLine` is unexported, either export it for testing or write the test in the same package. Prefer same package: `package commands` with a `_test.go` file.

Problem: `executeLine` takes a `*db.DB` but doesn't return the created task. To support back-references, `executeLine` (or a wrapper) needs to return the task ID when the command is `add`.

```go
func TestBatchBackReferences(t *testing.T) {
    d := openTestDB(t) // helper that opens a temp DB
    defer d.Close()

    input := `add "Parent" -d "parent task"
add "Child" -d "child" -p $1
dep $1 $2`

    // Execute batch and verify parent-child relationship + dep wiring.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/dt/commands/ -run TestBatchBackReferences -v`
Expected: Compilation error or failure (no back-reference support yet)

- [ ] **Step 3: Implement back-reference support**

Modify `batch.go`:

1. Change `executeLine` signature to return `(string, error)` where the string is the task ID for `add` commands (empty string for other commands).

2. In the `NewBatchCmd` `Run` function, maintain a `refs []string` slice. After each `executeLine` call, if a non-empty ID is returned, append it to `refs`.

3. Before calling `executeLine`, substitute `$N` references in the line. Add a helper:

```go
func substituteRefs(line string, refs []string) (string, error) {
    // Find all $N patterns (where N is a positive integer).
    // Replace with refs[N-1]. Error if N is out of range.
    // Use regexp or simple string scanning.
}
```

4. Update `batchAdd` to return the created task's ID. Change signature to `batchAdd(database *db.DB, args []string) (string, error)`. Pass the ID back through `executeLine`.

5. Update all other command handlers in `executeLine` to return `("", err)`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/dt/commands/ -run TestBatchBackReferences -v`
Expected: PASS

- [ ] **Step 5: Write test for invalid back-reference**

Test that `$99` when only 1 task has been added returns an error. Test that `$0` returns an error (1-indexed).

- [ ] **Step 6: Run test to verify it fails, implement, verify it passes**

Run: `go test ./cmd/dt/commands/ -run TestBatchBackReference -v`

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`
Expected: All tests pass (existing batch behavior unchanged for lines without `$N`).

- [ ] **Step 8: Commit**

```bash
git add cmd/dt/commands/batch.go cmd/dt/commands/batch_test.go
git commit -m "feat: add back-reference support to dt batch ($1, $2, etc.)"
```

---

### Task 2: DB layer — exclude parent tasks from ready queries

Parent tasks (tasks that have children) should never appear in `ReadyTasks()` results. The daemon should not spawn workers for them.

**Files:**
- Modify: `internal/db/tasks.go:218-243` (`ReadyTasks` query)
- Test: `internal/db/db_test.go` (add test cases)

- [ ] **Step 1: Write failing test**

Add to `db_test.go`:

```go
func TestReadyTasks_ExcludesParentTasks(t *testing.T) {
    d := openTestDB(t)
    parent, _ := d.AddTask("parent", "desc", "", "")
    d.AddTask("child1", "desc", parent.ID, "")
    d.AddTask("child2", "desc", parent.ID, "")

    ready, _ := d.ReadyTasks()
    for _, task := range ready {
        if task.ID == parent.ID {
            t.Errorf("parent task %s should not appear in ready tasks", parent.ID)
        }
    }
    // children should be ready
    if len(ready) != 2 {
        t.Errorf("expected 2 ready tasks, got %d", len(ready))
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestReadyTasks_ExcludesParentTasks -v`
Expected: FAIL (parent appears in ready list)

- [ ] **Step 3: Add NOT EXISTS subquery to ReadyTasks**

In `tasks.go`, modify the `ReadyTasks` query to add:
```sql
AND NOT EXISTS (
    SELECT 1 FROM tasks child WHERE child.parent_id = t.id
)
```

This excludes any task that has children, regardless of children's status.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestReadyTasks_ExcludesParentTasks -v`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/db/tasks.go internal/db/db_test.go
git commit -m "feat: exclude parent tasks from ReadyTasks results"
```

---

### Task 3: DB layer — auto-complete parent when all children done

When a child task is marked done, check if all siblings are also done. If so, mark the parent as done.

**Files:**
- Modify: `internal/db/tasks.go` (`DoneTask` method)
- Test: `internal/db/db_test.go` (add test cases)

- [ ] **Step 1: Write failing test — parent auto-completes**

```go
func TestDoneTask_AutoCompletesParent(t *testing.T) {
    d := openTestDB(t)
    parent, _ := d.AddTask("parent", "", "", "")
    child1, _ := d.AddTask("child1", "", parent.ID, "")
    child2, _ := d.AddTask("child2", "", parent.ID, "")

    d.DoneTask(child1.ID)
    p, _ := d.GetTask(parent.ID)
    if p.Status == "done" {
        t.Error("parent should not be done yet (child2 still open)")
    }

    d.DoneTask(child2.ID)
    p, _ = d.GetTask(parent.ID)
    if p.Status != "done" {
        t.Errorf("parent status = %s, want done (all children done)", p.Status)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/db/ -run TestDoneTask_AutoCompletesParent -v`
Expected: FAIL (parent stays open)

- [ ] **Step 3: Implement auto-complete in DoneTask**

At the end of `DoneTask`, after marking the task done, check if the task has a parent:

```go
// Auto-complete parent if all children are done.
if task.ParentID != nil {
    var notDone int
    err := d.q.QueryRow(
        `SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND status != 'done'`,
        *task.ParentID,
    ).Scan(&notDone)
    if err == nil && notDone == 0 {
        d.DoneTask(*task.ParentID) // recursive call for nested parents
    }
}
```

Note: The recursive call handles potential multi-level nesting, but for now we only have one level (plan parent → children). The `task` variable here is the task *before* the status change, so `ParentID` is already available.

Actually, we need to re-fetch the task after the update, or just use the `ParentID` we already have. The current `DoneTask` fetches the task at the start for the old status, so `task.ParentID` is available.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/db/ -run TestDoneTask_AutoCompletesParent -v`
Expected: PASS

- [ ] **Step 5: Write test — parent not completed when some children still open**

```go
func TestDoneTask_ParentNotCompletedWithOpenChildren(t *testing.T) {
    d := openTestDB(t)
    parent, _ := d.AddTask("parent", "", "", "")
    child1, _ := d.AddTask("child1", "", parent.ID, "")
    d.AddTask("child2", "", parent.ID, "")
    d.AddTask("child3", "", parent.ID, "")

    d.DoneTask(child1.ID)
    p, _ := d.GetTask(parent.ID)
    if p.Status == "done" {
        t.Error("parent should not be done (2 children still open)")
    }
}
```

- [ ] **Step 6: Run test to verify it passes (should already pass)**

Run: `go test ./internal/db/ -run TestDoneTask_ParentNot -v`
Expected: PASS

- [ ] **Step 7: Run full test suite**

Run: `go test ./...`

- [ ] **Step 8: Commit**

```bash
git add internal/db/tasks.go internal/db/db_test.go
git commit -m "feat: auto-complete parent task when all children are done"
```

---

### Task 4: Daemon — parent-branch-aware spawning

When spawning a child task, the daemon should:
1. Detect the parent task
2. Create the parent branch (`dispatch/plan-<parentID>`) lazily if it doesn't exist
3. Branch the child's worktree from the parent branch instead of the base branch

**Files:**
- Modify: `internal/daemon/daemon.go:152-211` (`spawnReady`)
- Modify: `internal/daemon/worktree.go` (add `BranchExists` helper)
- Test: `internal/daemon/daemon_test.go` (add test cases)

- [ ] **Step 1: Add `BranchExists` to worktree.go**

```go
func BranchExists(repoDir, branchName string) bool {
    cmd := exec.Command("git", "rev-parse", "--verify", branchName)
    cmd.Dir = repoDir
    return cmd.Run() == nil
}
```

- [ ] **Step 2: Write failing test for parent-branch-aware spawning**

```go
func TestDaemon_SpawnChildUsesParentBranch(t *testing.T) {
    d := openTestDB(t)
    repoDir := initTestRepo(t)
    worktreeBase := filepath.Join(t.TempDir(), "worktrees")

    parent, _ := d.AddTask("parent plan", "meta", "", "")
    child, _ := d.AddTask("child task", "do work", parent.ID, "")

    spawner := &MockSpawner{ExitCode: 0}
    daemon := New(d, Config{
        MaxWorkers:   4,
        RepoPath:     repoDir,
        WorktreeBase: worktreeBase,
    }, spawner)

    daemon.spawnReady()

    // Parent should NOT be spawned (has children).
    // Child should be spawned.
    if len(spawner.Spawned) != 1 {
        t.Fatalf("expected 1 spawn, got %d", len(spawner.Spawned))
    }
    if spawner.Spawned[0].ID != child.ID {
        t.Errorf("spawned task = %s, want %s", spawner.Spawned[0].ID, child.ID)
    }

    // Parent branch should exist.
    parentBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
    if !BranchExists(repoDir, parentBranch) {
        t.Errorf("parent branch %s should exist", parentBranch)
    }
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/daemon/ -run TestDaemon_SpawnChildUsesParentBranch -v`
Expected: FAIL (parent gets spawned, no parent branch created)

Note: The ReadyTasks filter from Task 2 prevents the parent from appearing, so the parent won't be spawned. But the parent branch won't exist yet — that's the failing part.

- [ ] **Step 4: Implement parent-branch-aware spawning in spawnReady**

In `daemon.go`, modify `spawnReady` between the claim and worktree creation:

```go
// Determine which branch to base the worktree on.
baseBranch := d.baseBranch
if task.ParentID != nil {
    // This is a child task — branch from the parent's plan branch.
    parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
    if !BranchExists(d.repoPath, parentBranch) {
        // Lazily create the parent branch from base.
        base := d.baseBranch
        if base == "" {
            base, _ = DetectDefaultBranch(d.repoPath)
        }
        cmd := exec.Command("git", "branch", parentBranch, base)
        cmd.Dir = d.repoPath
        if out, err := cmd.CombinedOutput(); err != nil {
            d.logger.Printf("spawn: create parent branch %s: %v\n%s", parentBranch, err, out)
            d.db.ReleaseTask(task.ID)
            continue
        }
    }
    baseBranch = parentBranch
}

// Create worktree (existing code, but now using baseBranch).
wtDir := filepath.Join(d.worktreeBase, task.ID)
branchName := fmt.Sprintf("dispatch/%s", task.ID)
if err := CreateWorktree(d.repoPath, wtDir, branchName, baseBranch); err != nil {
```

Note: need to add `"os/exec"` to imports if not already present.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestDaemon_SpawnChildUsesParentBranch -v`
Expected: PASS

- [ ] **Step 6: Run full test suite**

Run: `go test ./...`

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/worktree.go internal/daemon/daemon_test.go
git commit -m "feat: spawn child tasks from parent plan branch"
```

---

### Task 5: Daemon — merge child branch into parent on completion

When a child task completes, merge its branch into the parent branch instead of deleting it. Block on merge conflicts, preserving the branch and worktree for human resolution.

**Files:**
- Modify: `internal/daemon/daemon.go:246-286` (`monitorWorkers`)
- Modify: `internal/daemon/worktree.go` (add `MergeBranch`)
- Test: `internal/daemon/daemon_test.go`
- Test: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Add `MergeBranch` to worktree.go**

```go
// MergeBranch merges sourceBranch into targetBranch.
// Returns nil on success, error with conflict details on failure.
func MergeBranch(repoDir, sourceBranch, targetBranch string) error {
    // Checkout target branch in a temporary worktree to perform the merge.
    // We can't merge in the main repo if it might have a different branch checked out.
    // Alternative: use git merge-tree (plumbing) or a temporary worktree.
    //
    // Simplest approach: create a temp worktree for the target branch,
    // merge there, then remove the temp worktree.
    tmpDir, err := os.MkdirTemp("", "dispatch-merge-*")
    if err != nil {
        return fmt.Errorf("create temp dir: %w", err)
    }
    defer os.RemoveAll(tmpDir)

    // Checkout target branch in temp worktree.
    cmd := exec.Command("git", "worktree", "add", tmpDir, targetBranch)
    cmd.Dir = repoDir
    if out, err := cmd.CombinedOutput(); err != nil {
        return fmt.Errorf("checkout target: %w\n%s", err, out)
    }
    defer func() {
        exec.Command("git", "worktree", "remove", tmpDir, "--force").Run()
    }()

    // Merge source into target.
    cmd = exec.Command("git", "merge", sourceBranch, "--no-edit")
    cmd.Dir = tmpDir
    if out, err := cmd.CombinedOutput(); err != nil {
        // Abort the failed merge.
        abort := exec.Command("git", "merge", "--abort")
        abort.Dir = tmpDir
        abort.Run()
        return fmt.Errorf("merge conflict: %s into %s:\n%s", sourceBranch, targetBranch, out)
    }
    return nil
}
```

- [ ] **Step 2: Write test for MergeBranch — clean merge**

In `worktree_test.go`:

```go
func TestMergeBranch_Clean(t *testing.T) {
    repoDir := initTestRepo(t)
    // Create a branch, add a file, merge back.
    // ...setup, verify merge succeeds...
}
```

Set up: create branch `feature`, add a file there, call `MergeBranch(repoDir, "feature", "main")`, verify the file exists on main.

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestMergeBranch_Clean -v`

- [ ] **Step 4: Write test for MergeBranch — conflict**

Create two branches that modify the same file differently. Verify `MergeBranch` returns an error containing "merge conflict".

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/daemon/ -run TestMergeBranch_Conflict -v`

- [ ] **Step 6: Modify monitorWorkers for merge-on-completion**

In `daemon.go`, replace the clean exit branch of `monitorWorkers` (around line 253).

**Critical ordering:** For child tasks, merge into the parent branch *before* calling `DoneTask`. This ensures dependent tasks won't be picked up by `spawnReady` until the blocker's work is on the parent branch. If the merge fails, block the task without ever marking it done.

```go
if f.err == nil {
    task, err := d.db.GetTask(f.taskID)
    if err != nil {
        d.logger.Printf("monitor: get task %s: %v", f.taskID, err)
        continue
    }

    wtDir := filepath.Join(d.worktreeBase, f.taskID)
    branchName := fmt.Sprintf("dispatch/%s", f.taskID)

    if task.ParentID != nil {
        // Child task — merge into parent branch BEFORE marking done.
        // This ensures dependent tasks see the blocker's work when they branch
        // from the parent branch.
        parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
        if err := MergeBranch(d.repoPath, branchName, parentBranch); err != nil {
            d.logger.Printf("monitor: merge %s into %s failed: %v", branchName, parentBranch, err)
            reason := fmt.Sprintf("Merge conflict merging into plan branch:\n%v", err)
            if _, err := d.db.BlockTask(f.taskID, reason); err != nil {
                d.logger.Printf("monitor: block task %s: %v", f.taskID, err)
            }
            // Preserve branch and worktree for human resolution.
            continue
        }

        // Merge succeeded — now mark done (which may auto-complete the parent).
        if task.Status != "done" {
            if _, err := d.db.DoneTask(f.taskID); err != nil {
                d.logger.Printf("monitor: done task %s: %v", f.taskID, err)
            }
        }

        // Clean merge — remove child branch and worktree.
        if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
            d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
        }
    } else {
        // Standalone task (no parent) — original behavior.
        if task.Status != "done" {
            if _, err := d.db.DoneTask(f.taskID); err != nil {
                d.logger.Printf("monitor: done task %s: %v", f.taskID, err)
            }
        }
        if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
            d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
        }
    }
    d.logger.Printf("task %s completed", f.taskID)
}
```

- [ ] **Step 7: Write integration test for merge-on-completion**

In `daemon_test.go`, test the full flow: create parent + child, spawn child, child completes, verify child branch merged into parent branch.

- [ ] **Step 8: Run tests**

Run: `go test ./internal/daemon/ -v`
Expected: All pass.

- [ ] **Step 9: Run full test suite**

Run: `go test ./...`

- [ ] **Step 10: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/worktree.go internal/daemon/daemon_test.go internal/daemon/worktree_test.go
git commit -m "feat: merge child branches into parent plan branch on completion"
```

---

### Task 6: Integration test — full plan lifecycle

End-to-end test: create a parent with multiple children (some parallel, some dependent), run the daemon, verify merge order and parent auto-completion.

**Files:**
- Modify: `tests/daemon_integration_test.go`

- [ ] **Step 1: Write integration test**

```go
func TestDaemonIntegration_PlanLifecycle(t *testing.T) {
    repoDir := initGitRepo(t)
    dbPath := filepath.Join(t.TempDir(), "test.db")
    worktreeBase := filepath.Join(t.TempDir(), "worktrees")

    database, _ := db.Open(dbPath)
    defer database.Close()

    // Create plan structure: parent with 3 children.
    // child1 and child2 are parallel, child3 depends on child1.
    parent, _ := database.AddTask("Plan: test feature", "meta", "", "")
    child1, _ := database.AddTask("child 1", "parallel work", parent.ID, "")
    child2, _ := database.AddTask("child 2", "parallel work", parent.ID, "")
    child3, _ := database.AddTask("child 3", "depends on child1", parent.ID, "")
    database.AddDep(child1.ID, child3.ID)

    // Use a spawner that calls dt done immediately.
    spawner := &doneCallingSpawner{db: database}

    d := daemon.New(database, daemon.Config{
        MaxWorkers:   4,
        PollInterval: 100 * time.Millisecond,
        RepoPath:     repoDir,
        WorktreeBase: worktreeBase,
    }, spawner)

    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    defer cancel()

    done := make(chan error, 1)
    go func() { done <- d.Run(ctx) }()

    // Wait for parent to auto-complete.
    deadline := time.After(8 * time.Second)
    for {
        select {
        case <-deadline:
            cancel()
            t.Fatal("plan not completed within timeout")
        case <-time.After(200 * time.Millisecond):
            p, _ := database.GetTask(parent.ID)
            if p.Status == "done" {
                cancel()
                <-done

                // Verify parent branch exists and contains all work.
                parentBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
                if !daemon.BranchExists(repoDir, parentBranch) {
                    t.Error("parent branch should exist after plan completion")
                }

                // Verify all children are done.
                for _, id := range []string{child1.ID, child2.ID, child3.ID} {
                    c, _ := database.GetTask(id)
                    if c.Status != "done" {
                        t.Errorf("child %s status = %s, want done", id, c.Status)
                    }
                }
                return
            }
        }
    }
}
```

- [ ] **Step 2: Run test**

Run: `go test ./tests/ -run TestDaemonIntegration_PlanLifecycle -v -timeout 30s`
Expected: PASS

- [ ] **Step 3: Write integration test for merge conflict**

Create two parallel children that modify the same file. Verify one succeeds and the other gets blocked with a merge conflict reason.

Note: this requires a spawner that actually creates file changes in the worktree before calling `dt done`. This is more complex — the spawner needs access to the worktree path. Use a custom spawner that writes a conflicting file.

```go
type fileWritingSpawner struct {
    db       *db.DB
    filename string
    content  func(task db.Task) string // different content per task to cause conflict
}
```

- [ ] **Step 4: Run test**

Run: `go test ./tests/ -run TestDaemonIntegration_MergeConflict -v -timeout 30s`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `go test ./...`
Expected: All pass.

- [ ] **Step 6: Commit**

```bash
git add tests/daemon_integration_test.go
git commit -m "test: add integration tests for plan lifecycle and merge conflicts"
```

---

### Task 7: Update PRD

Update the PRD to reflect the planner design decisions and Phase 3 scope changes.

**Files:**
- Modify: `dispatch-prd_1.md`

- [ ] **Step 1: Update Section 2.3 (Prompt files) — planner.md**

Replace the current `planner.md` section (lines 263-274) with expanded description referencing the design spec. Note that the planner is delivered as a superpowers skill, not just a prompt file.

- [ ] **Step 2: Update Phase 3 scope (Section 5)**

Replace the current Phase 3 description (lines 355-359) with the expanded scope:
- `worker.md` system prompt
- `triage.md` system prompt + triage flow
- `dispatch-planner` skill
- Session logging to disk
- Parent task / merge model (parent branch, merge-on-completion, auto-complete)
- `dt batch` back-references

Update exit criteria to include planner and merge model criteria from the spec.

- [ ] **Step 3: Update worktree cleanup (Section 2.2)**

Add note that child task branches merge into parent branch on clean exit rather than being deleted. Standalone tasks (no parent) retain original behavior.

- [ ] **Step 4: Add Phase 3 implementation notes section**

Add a new section (Section 10 or similar) for Phase 3 implementation notes, documenting:
- `dt batch` back-reference syntax (`$1`, `$2`)
- Parent branch naming convention (`dispatch/plan-<id>`)
- Merge-on-completion behavior
- Parent auto-completion

- [ ] **Step 5: Note chunks/epics partial supersession**

In the Future phases section, add a note that the parent task / merge model partially addresses the chunks concept.

- [ ] **Step 6: Commit**

```bash
git add dispatch-prd_1.md
git commit -m "docs: update PRD with Phase 3 planner and merge model scope"
```

---

### Task 8: Create the dispatch-planner skill

Write the actual superpowers skill file that will be invoked to decompose specs into tasks.

**Files:**
- Create: skill file (location TBD — either project-local or in a skills directory)

This task depends on Tasks 1-6 being complete, since the skill references `dt batch` back-references and the parent task merge model.

- [ ] **Step 1: Determine skill location**

Check where project-local skills live. If using superpowers, skills may go in a `.claude/skills/` directory or similar. Check the project's superpowers configuration.

- [ ] **Step 2: Write the skill file**

Create `dispatch-planner.md` with the following structure based on the design spec:

```markdown
---
name: dispatch-planner
description: Decompose an approved spec into parallel dt tasks with dependency wiring and a parent plan branch for merging completed work
---

# Dispatch Planner

**Announce:** "Using dispatch-planner to decompose the spec into parallel tasks."

**Type:** Rigid — follow this process exactly.

## Preconditions

- A spec document exists. If the path isn't clear from conversation context, ask.
- `dt` is on PATH. Verify with `dt --help`.

## Process

### Phase 1: Analysis

1. Read the spec document.
2. Read the codebase areas the spec references. Use Grep/Glob/Read to understand current state.
3. Build a mental model: what files change, what new files are created, what depends on what.

### Phase 2: Parallelism Rationale

Present to the user:

- **Work areas identified** — the major logical groupings of changes
- **Parallel work** — which areas can run concurrently and why (different files/subsystems)
- **Sequential work** — which areas must be ordered and why (schema before code, interface before implementation)
- **Risks** — any areas where parallel work might produce merge conflicts

**GATE: User must approve the parallelism structure before proceeding.**

### Phase 3: Task List

For each task, present:

- **Title** — short, descriptive
- **Description** containing:
  - What to do (1-2 sentences)
  - Scope boundary (what's in, what's explicitly out)
  - Expected footprint (files to create/modify, rough line estimates)
  - How to verify (test command or acceptance check)
  - Context pointers (spec sections, related task references)
- **Dependencies** — which tasks block this one and why

**Sizing guidance:** Size by scope coherence (one logical area) and decision density (non-obvious choices). Heuristic: >10 files or >300 lines of non-trivial logic means consider splitting. Tests and boilerplate don't count against this. The test: can a worker hold the full context and execute reliably?

**GATE: User must approve the task list before proceeding.**

### Phase 4: Review Loop

Dispatch a reviewer subagent (Agent tool, general-purpose) with this prompt:

> Review this task decomposition for a dispatch plan. Check for:
> 1. Overlapping files between tasks marked as parallel (merge conflict risk)
> 2. Tasks with scattered scope or too many independent decisions
> 3. Missing dependencies (task B needs task A's output but no dep wired)
> 4. Underspecified scope (description doesn't make clear what's in/out)
>
> Task list: [paste the approved task list]
> Spec: [spec file path]

Fix issues identified by the reviewer. Re-dispatch review, max 3 iterations. Surface to human if unresolved.

### Phase 5: Create

Generate `dt batch` input and execute. Format:

```
add "Plan: <spec title>" -d "<plan-level description>"
add "<task 1>" -d "<description>" -p $1
add "<task 2>" -d "<description>" -p $1
dep $2 $3
```

Run via: `echo '<batch input>' | dt batch`

Verify with: `dt list --tree` and `dt show <parent-id>`

## What This Skill Does NOT Do

- Write implementation plans or code snippets
- Create intermediate documents between spec and tasks
- Implement anything
- Manage chunks or epics beyond the parent task grouping
```

- [ ] **Step 3: Test the skill invocation**

Verify the skill can be invoked with `/dispatch-planner` in a Claude Code session. Check that it loads correctly and presents the announcement.

- [ ] **Step 4: Commit**

```bash
git add <skill-file-path>
git commit -m "feat: add dispatch-planner skill for spec-to-task decomposition"
```
