# Dispatch GP Hook Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `--gp` flag to `dispatchd` that calls `gp sync-child <task-id>` on child task completion, enabling GraphPilot to track dispatch progress in real time.

**Architecture:** Add a `GPEnabled` boolean to `daemon.Config`, wired via `--gp` flag / `DISPATCH_GP` env var. On startup, check `gp` is in PATH (warn and disable if not). After `DoneTask()` succeeds in `handleReviewApproval`, fire-and-forget `gp sync-child <task-id>`. Never block or fail dispatch operations on GP errors.

**Tech Stack:** Go 1.22, cobra CLI, os/exec for external command invocation.

**Spec:** `~/Documents/graph-pilot/docs/superpowers/specs/2026-03-24-gp-dispatch-integration-design.md` (sections 5.1 and 5.2)

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `internal/daemon/daemon.go` | Modify | Add `GPEnabled` to Config, `gpBin` to Daemon, startup validation, sync-child call in `handleReviewApproval` |
| `internal/daemon/daemon_test.go` | Modify | Tests for GP sync-child integration |
| `cmd/dispatchd/main.go` | Modify | Add `--gp` flag and `DISPATCH_GP` env var |
| `cmd/dt/commands/batch.go` | Modify | Add `findPlanParent` helper, GP dispatch wiring after commit |
| `cmd/dt/commands/batch_test.go` | Modify/Create | Tests for `findPlanParent` and batch GP wiring |

---

### Task 1: Add `--gp` Flag and Config

**Files:**
- Modify: `cmd/dispatchd/main.go:133-140` (init function, add flag)
- Modify: `cmd/dispatchd/main.go:49-106` (RunE, read flag and pass to config)
- Modify: `internal/daemon/daemon.go:20-27` (Config struct)

- [ ] **Step 1: Add `GPEnabled` to `daemon.Config`**

In `internal/daemon/daemon.go`, add to the `Config` struct:

```go
type Config struct {
	DBPath       string
	Repos        map[string]config.RepoConfig
	BaseBranch   string
	PollInterval time.Duration
	WorktreeBase string
	SessionDir   string
	GPEnabled    bool // Enable GraphPilot integration (gp sync-child on task completion)
}
```

- [ ] **Step 2: Add `--gp` flag to dispatchd**

In `cmd/dispatchd/main.go`, add to the `init()` function:

```go
rootCmd.Flags().Bool("gp", os.Getenv("DISPATCH_GP") == "1", "enable GraphPilot integration (env: DISPATCH_GP=1)")
```

- [ ] **Step 3: Read flag and pass to config**

In `cmd/dispatchd/main.go`, in the `RunE` function, after the other flag reads (~line 55):

```go
gpEnabled, _ := cmd.Flags().GetBool("gp")
```

And add to the `cfg` struct initialization (~line 99):

```go
cfg := daemon.Config{
	// ... existing fields ...
	GPEnabled:    gpEnabled,
}
```

- [ ] **Step 4: Build and verify**

Run: `go build ./...`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go cmd/dispatchd/main.go
git commit -m "feat: add --gp flag and GPEnabled config for GraphPilot integration"
```

---

### Task 2: GP Binary Validation on Startup

**Files:**
- Modify: `internal/daemon/daemon.go:41-76` (Daemon struct and New function)

- [ ] **Step 1: Add `gpBin` field to Daemon struct**

In `internal/daemon/daemon.go`, add to the `Daemon` struct:

```go
type Daemon struct {
	// ... existing fields ...
	gpBin string // path to gp binary, empty if GP integration disabled
}
```

- [ ] **Step 2: Validate gp binary in `New()`**

Note: `os/exec` is already imported in `daemon.go` — no new import needed.

In `internal/daemon/daemon.go`, in the `New()` function, after the existing initialization and before the return:

```go
	d := &Daemon{
		// ... existing fields ...
	}

	if cfg.GPEnabled {
		gpPath, err := exec.LookPath("gp")
		if err != nil {
			d.logger.Printf("WARNING: --gp enabled but gp binary not found in PATH; disabling GP integration")
		} else {
			d.gpBin = gpPath
			d.logger.Printf("GraphPilot integration enabled (gp at %s)", gpPath)
		}
	}

	return d
```

- [ ] **Step 3: Build and verify**

Run: `go build ./...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/daemon.go
git commit -m "feat: validate gp binary on startup, warn and disable if not found"
```

---

### Task 3: Fire `gp sync-child` on Task Completion

**Files:**
- Modify: `internal/daemon/daemon.go:410-457` (handleReviewApproval function)

This is the core integration point. After `DoneTask()` succeeds, call `gp sync-child <task-id>` in a fire-and-forget goroutine.

- [ ] **Step 1: Add `gpSyncChild` helper method**

In `internal/daemon/daemon.go`, add a new method (place it after `handleReviewApproval`):

```go
// gpSyncChild notifies GraphPilot that a dispatch task completed.
// Fire-and-forget: never blocks or fails dispatch operations.
// Safe to call after handleReviewApproval deletes daemon maps — taskID is
// captured by value, and no other daemon state is accessed in the goroutine.
func (d *Daemon) gpSyncChild(taskID string) {
	if d.gpBin == "" {
		return
	}
	go func() {
		cmd := exec.Command(d.gpBin, "sync-child", taskID)
		if out, err := cmd.CombinedOutput(); err != nil {
			d.logger.Printf("gp sync-child %s failed: %v (output: %s)", taskID, err, strings.TrimSpace(string(out)))
		}
	}()
}
```

- [ ] **Step 2: Call `gpSyncChild` in `handleReviewApproval`**

In `internal/daemon/daemon.go`, in `handleReviewApproval`, add the call after each `DoneTask()` succeeds. There are two paths — with parent and without parent.

**With parent (line ~431):** The existing code logs on error and checks `ac` separately. `ac` (auto-complete) is only non-nil when `DoneTask` succeeds, so adding an `else` for `gpSyncChild` doesn't affect the `ac` check — but keep the `ac` check outside the if/else to preserve clarity:

```go
		if task.Status != "done" {
			_, ac, err := d.db.DoneTask(taskID)
			if err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			} else {
				d.gpSyncChild(taskID)
			}
			if ac != nil {
				d.triggerPR(ac)
			}
		}
```

**Without parent (line ~444):**
```go
		if task.Status != "done" {
			if _, _, err := d.db.DoneTask(taskID); err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			} else {
				d.gpSyncChild(taskID)
			}
		}
```

The key change in both paths: wrap the existing `DoneTask` error check in an `if/else` so we only call `gpSyncChild` when `DoneTask` succeeded. The `ac != nil` check stays outside the if/else because `ac` is declared in the outer scope and is nil on error anyway.

- [ ] **Step 3: Build and verify**

Run: `go build ./...`
Expected: No errors

- [ ] **Step 4: Commit**

```bash
git add internal/daemon/daemon.go
git commit -m "feat: fire gp sync-child on task completion (fire-and-forget)"
```

---

### Task 4: Add Test for GP Sync Behavior

**Files:**
- Modify: `internal/daemon/daemon_test.go` (add test case)

- [ ] **Step 1: Write test verifying gpSyncChild is called on task completion**

Add a test that:
1. Creates a daemon with `GPEnabled: true` and a mock gp binary (a shell script that writes the task ID to a temp file)
2. Sets up a task that completes successfully
3. Verifies the mock gp binary was invoked with `sync-child <task-id>`

The exact test structure depends on the existing test patterns in `daemon_test.go`. Read the existing tests first to match the style.

Key assertions:
- When `GPEnabled: false`, no external command is called
- When `GPEnabled: true` but `gp` not in PATH, daemon starts without error and logs warning
- When `GPEnabled: true` and `gp` in PATH, `gp sync-child` is called after task completion

- [ ] **Step 2: Run tests**

Run: `go test ./internal/daemon/ -v -run TestGP`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add internal/daemon/daemon_test.go
git commit -m "test: add tests for GP sync-child integration"
```

---

### Task 5: Auto-wire GP Graph After `dt batch`

**Files:**
- Modify: `cmd/dt/commands/batch.go:96-104` (after commit, before success output)

When `GRAPHPILOT_NODE` is set in the environment and the batch created tasks, automatically call `gp dispatch $GRAPHPILOT_NODE --plan <parent-task-id>` to wire the GP graph.

`GRAPHPILOT_NODE` is the GP node ID of the *calling* GraphPilot node — i.e., the node that dispatched this batch. GP sets this env var when it spawns a dispatch-planner subprocess. The `gp dispatch` call tells GP "this node's work has been decomposed into dispatch tasks rooted at `<parent-task-id>`."

The `refs` slice contains all task IDs created by `add` commands during the batch. Rather than assuming `refs[0]` is the parent (which only holds if the batch file starts with the parent add), we look up each ref in the DB and find the one with children — that's the plan parent.

- [ ] **Step 1: Add `findPlanParent` helper**

In `cmd/dt/commands/batch.go`, add a helper that finds the parent task among a set of refs:

```go
// findPlanParent returns the ID of the task in refs that has children (is a
// parent). Returns "" if no parent is found among the refs.
func findPlanParent(database *db.DB, refs []string) string {
	refSet := make(map[string]bool, len(refs))
	for _, id := range refs {
		refSet[id] = true
	}
	for _, id := range refs {
		task, err := database.GetTask(id)
		if err != nil {
			continue
		}
		if task.ParentID == nil && len(refs) > 1 {
			// A top-level task created alongside others — likely the parent.
			// Confirm by checking if any other ref has this as parent.
			for _, otherID := range refs {
				if otherID == id {
					continue
				}
				other, err := database.GetTask(otherID)
				if err != nil {
					continue
				}
				if other.ParentID != nil && *other.ParentID == id {
					return id
				}
			}
		}
	}
	return ""
}
```

- [ ] **Step 2: Add GP dispatch logic after batch commit**

In `cmd/dt/commands/batch.go`, after the `tx.Commit()` succeeds (line ~98) and before the success output, add:

```go
			// GraphPilot integration: wire GP graph if GRAPHPILOT_NODE is set.
			// GRAPHPILOT_NODE is the GP node ID of the calling node — set by GP
			// when it spawns a dispatch-planner. This tells GP that the node's
			// work has been decomposed into dispatch tasks.
			if gpNode := os.Getenv("GRAPHPILOT_NODE"); gpNode != "" && len(refs) > 0 {
				parentID := findPlanParent(d, refs)
				if parentID == "" {
					fmt.Fprintf(os.Stderr, "warning: GRAPHPILOT_NODE set but no plan parent found among batch refs; skipping GP wiring\n")
				} else {
					gpBin, err := exec.LookPath("gp")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: GRAPHPILOT_NODE set but gp not in PATH; skipping GP wiring\n")
					} else {
						gpCmd := exec.Command(gpBin, "dispatch", gpNode, "--plan", parentID)
						if out, err := gpCmd.CombinedOutput(); err != nil {
							fmt.Fprintf(os.Stderr, "warning: gp dispatch failed: %v\n%s\n", err, string(out))
						} else {
							fmt.Printf("GP: wired %s to dispatch plan %s\n", gpNode, parentID)
						}
					}
				}
			}
```

- [ ] **Step 3: Add imports**

Add to the import block in `batch.go`:

```go
	"os/exec"
```

(`os` is already imported.)

- [ ] **Step 4: Build and verify**

Run: `go build ./...`
Expected: No errors

- [ ] **Step 5: Commit**

```bash
git add cmd/dt/commands/batch.go
git commit -m "feat: auto-wire GP graph after dt batch when GRAPHPILOT_NODE is set"
```

---

### Task 6: Add Test for Batch GP Wiring

**Files:**
- Modify: `cmd/dt/commands/batch_test.go` (add test case, or create if it doesn't exist)

- [ ] **Step 1: Write test for `findPlanParent`**

Test that `findPlanParent` correctly identifies the parent task:
1. Create a DB with a parent task and two child tasks referencing it
2. Pass all three IDs as refs (in various orders — parent first, parent last)
3. Assert it returns the parent ID in all cases
4. Assert it returns `""` when refs is empty or contains only orphan tasks

- [ ] **Step 2: Write test for GP wiring env var behavior**

Test the batch command's GP integration (can be a lightweight integration test or unit test of the helper):
- When `GRAPHPILOT_NODE` is not set, no GP command is attempted
- When `GRAPHPILOT_NODE` is set but no parent exists in refs, warning is printed

- [ ] **Step 3: Run tests**

Run: `go test ./cmd/dt/commands/ -v -run TestGP`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/dt/commands/batch_test.go
git commit -m "test: add tests for batch GP wiring and findPlanParent"
```

---

## Task Dependency Order

```
Task 1 (Flag + Config) → Task 2 (Startup validation) → Task 3 (Sync call) → Task 4 (Tests)
Task 5 (Batch GP wiring) → Task 6 (Batch GP tests)
```

Tasks 1-4 are sequential (daemon changes). Tasks 5-6 are sequential (CLI change + tests). The two chains are independent of each other.
