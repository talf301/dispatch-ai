# Phase 3: Review Gates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add worker/reviewer system prompts, session logging to disk, and review gates to the dispatch daemon.

**Architecture:** The daemon's `monitorWorkers` loop gains a review phase between worker completion and merge/done. After a worker exits cleanly, the daemon spawns a reviewer in the same worktree. The `WorkerSpawner` interface gets a `role` parameter. Session output is tee'd to both the in-memory ring buffer and a log file on disk.

**Tech Stack:** Go, SQLite (existing), `internal/daemon` package, `internal/db` package.

---

### Task 1: Add `SpawnRole` type and update `WorkerSpawner` interface

**Files:**
- Modify: `internal/daemon/worker.go`
- Modify: `internal/daemon/mock_spawner.go`
- Modify: `internal/daemon/claude_spawner.go`
- Modify: `internal/daemon/claude_spawner_test.go`
- Modify: `internal/daemon/daemon.go` (call sites in `spawnReady`)
- Modify: `tests/daemon_integration_test.go` (all test spawner types)

- [ ] **Step 1: Add `SpawnRole` type to `worker.go`**

Add above the `WorkerSpawner` interface:

```go
// SpawnRole indicates whether a spawned process is a worker or reviewer.
type SpawnRole string

const (
	RoleWorker   SpawnRole = "worker"
	RoleReviewer SpawnRole = "reviewer"
)
```

Update the interface:

```go
type WorkerSpawner interface {
	Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole) (WorkerHandle, error)
}
```

- [ ] **Step 2: Update `MockSpawner.Spawn` signature**

In `mock_spawner.go`, add `role SpawnRole` parameter:

```go
func (m *MockSpawner) Spawn(_ context.Context, task db.Task, _ string, _ SpawnRole) (WorkerHandle, error) {
```

- [ ] **Step 3: Update `ClaudeSpawner`**

In `claude_spawner.go`:

Replace the `SystemPrompt` field with two prompt fields and select based on role:

```go
type ClaudeSpawner struct {
	ClaudeBin      string
	WorkerPrompt   string // contents of worker.md (with $TASK_ID placeholder)
	ReviewerPrompt string // contents of reviewer.md (with $TASK_ID placeholder)
	OutputLines    int
}
```

Update `Spawn` to accept `role SpawnRole` and select the prompt:

```go
func (s *ClaudeSpawner) Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole) (WorkerHandle, error) {
	// ... existing bin/lines defaults ...

	prompt := fmt.Sprintf("Your task ID is %s. Run `dt show %s` to read your assignment.", task.ID, task.ID)

	systemPrompt := s.WorkerPrompt
	if role == RoleReviewer {
		systemPrompt = s.ReviewerPrompt
	}
	// Substitute $TASK_ID in the system prompt.
	systemPrompt = strings.ReplaceAll(systemPrompt, "$TASK_ID", task.ID)

	args := []string{
		"--print",
		"--system-prompt", systemPrompt,
		"--prompt", prompt,
	}
	// ... rest unchanged ...
}
```

Add `"strings"` to the import block.

- [ ] **Step 4: Update `ClaudeSpawner` tests**

In `claude_spawner_test.go`, update both tests to pass role:

```go
// In TestClaudeSpawner_BuildsCommand:
spawner := &ClaudeSpawner{
	ClaudeBin:     fakeClaude,
	WorkerPrompt:  "You are a worker.",
	OutputLines:   10,
}
handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker)

// In TestClaudeSpawner_NonZeroExit:
spawner := &ClaudeSpawner{
	ClaudeBin:     fakeClaude,
	WorkerPrompt:  "You are a worker.",
	OutputLines:   10,
}
handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker)
```

- [ ] **Step 5: Update `spawnReady` call site in `daemon.go`**

In `daemon.go` line 212, change:

```go
handle, err := d.spawner.Spawn(ctx, task, wtDir)
```

to:

```go
handle, err := d.spawner.Spawn(ctx, task, wtDir, RoleWorker)
```

- [ ] **Step 6: Update all test spawners in `tests/daemon_integration_test.go`**

Add `_ daemon.SpawnRole` parameter to all test spawner `Spawn` methods:

- `doneCallingSpawner.Spawn`
- `fileCommittingSpawner.Spawn`
- `conflictingSpawner.Spawn`
- `hangingSpawner.Spawn`

- [ ] **Step 7: Run tests**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./...`
Expected: All tests pass.

- [ ] **Step 8: Commit**

```bash
git add internal/daemon/worker.go internal/daemon/mock_spawner.go internal/daemon/claude_spawner.go internal/daemon/claude_spawner_test.go internal/daemon/daemon.go tests/daemon_integration_test.go
git commit -m "feat: add SpawnRole to WorkerSpawner interface for worker/reviewer distinction"
```

---

### Task 2: Add session logging (`TeeWriter`)

**Files:**
- Create: `internal/daemon/teewriter.go`
- Create: `internal/daemon/teewriter_test.go`
- Modify: `internal/daemon/claude_spawner.go` (use `TeeWriter` instead of bare `RingBuf`)

- [ ] **Step 1: Write test for `TeeWriter`**

Create `internal/daemon/teewriter_test.go`:

```go
package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTeeWriter_WritesToBothDestinations(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "test.log")
	f, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}

	buf := NewRingBuf(10)
	tw := NewTeeWriter(buf, f)

	tw.Write([]byte("line one\nline two\n"))
	f.Close()

	// Check ring buffer got the data.
	if out := buf.String(); !strings.Contains(out, "line one") {
		t.Errorf("ring buffer missing data: %q", out)
	}

	// Check log file got the data.
	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "line one") {
		t.Errorf("log file missing data: %q", string(data))
	}
}

func TestTeeWriter_String_DelegatesToRingBuf(t *testing.T) {
	buf := NewRingBuf(10)
	tw := NewTeeWriter(buf, nil)

	tw.Write([]byte("hello\n"))

	if out := tw.String(); !strings.Contains(out, "hello") {
		t.Errorf("String() = %q, want it to contain %q", out, "hello")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -run TestTeeWriter -v`
Expected: FAIL — `NewTeeWriter` not defined.

- [ ] **Step 3: Implement `TeeWriter`**

Create `internal/daemon/teewriter.go`:

```go
package daemon

import (
	"io"
)

// TeeWriter writes to both a RingBuf (for in-memory crash context) and
// an optional io.Writer (for disk logging). It implements io.Writer and
// exposes String() for reading the ring buffer contents.
type TeeWriter struct {
	ring *RingBuf
	file io.Writer
}

// NewTeeWriter creates a TeeWriter. If file is nil, only the ring buffer is used.
func NewTeeWriter(ring *RingBuf, file io.Writer) *TeeWriter {
	return &TeeWriter{ring: ring, file: file}
}

func (t *TeeWriter) Write(p []byte) (int, error) {
	n, err := t.ring.Write(p)
	if err != nil {
		return n, err
	}
	if t.file != nil {
		t.file.Write(p)
	}
	return n, nil
}

// String returns the ring buffer contents.
func (t *TeeWriter) String() string {
	return t.ring.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -run TestTeeWriter -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/teewriter.go internal/daemon/teewriter_test.go
git commit -m "feat: add TeeWriter for dual ring-buffer and disk session logging"
```

---

### Task 3: Wire session logging into `ClaudeSpawner`

**Files:**
- Modify: `internal/daemon/claude_spawner.go`
- Modify: `internal/daemon/daemon.go` (create sessions dir, pass log path)

- [ ] **Step 1: Add `SessionDir` field to `ClaudeSpawner` and open log file in `Spawn`**

In `claude_spawner.go`, add `SessionDir` and `LogSuffix` fields:

```go
type ClaudeSpawner struct {
	ClaudeBin      string
	WorkerPrompt   string
	ReviewerPrompt string
	OutputLines    int
	SessionDir     string // path to ~/.dispatch/sessions/
	LogSuffix      string // suffix for log file, set by daemon per-spawn (e.g., "", "-2", "-review-1")
}
```

Update `Spawn` to create a log file and use `TeeWriter`. The daemon sets `LogSuffix` before each `Spawn` call to control the log file name:

```go
func (s *ClaudeSpawner) Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole) (WorkerHandle, error) {
	bin := s.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	lines := s.OutputLines
	if lines == 0 {
		lines = 100
	}

	prompt := fmt.Sprintf("Your task ID is %s. Run `dt show %s` to read your assignment.", task.ID, task.ID)

	systemPrompt := s.WorkerPrompt
	if role == RoleReviewer {
		systemPrompt = s.ReviewerPrompt
	}
	systemPrompt = strings.ReplaceAll(systemPrompt, "$TASK_ID", task.ID)

	args := []string{
		"--print",
		"--system-prompt", systemPrompt,
		"--prompt", prompt,
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir

	buf := NewRingBuf(lines)

	// Open session log file if SessionDir is set.
	var logFile *os.File
	if s.SessionDir != "" {
		logPath := filepath.Join(s.SessionDir, task.ID+s.LogSuffix+".log")
		var err error
		logFile, err = os.Create(logPath)
		if err != nil {
			return nil, fmt.Errorf("create session log: %w", err)
		}
	}

	tw := NewTeeWriter(buf, logFile)
	cmd.Stdout = tw
	cmd.Stderr = tw

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("start claude: %w", err)
	}

	h := &claudeHandle{cmd: cmd, tw: tw, logFile: logFile, done: make(chan struct{})}
	go func() {
		h.exitErr = cmd.Wait()
		if h.logFile != nil {
			h.logFile.Close()
		}
		close(h.done)
	}()

	return h, nil
}
```

Update `claudeHandle` to hold the `TeeWriter` and log file:

```go
type claudeHandle struct {
	cmd     *exec.Cmd
	tw      *TeeWriter
	logFile *os.File
	done    chan struct{}
	exitErr error
}
```

Update `Output()`:

```go
func (h *claudeHandle) Output() string { return h.tw.String() }
```

Add `"os"` and `"path/filepath"` to imports (if not already present).

- [ ] **Step 2: Create sessions directory on daemon startup**

In `daemon.go`, in the `Run` method, add before `recoverActive`:

```go
// Ensure sessions directory exists.
sessDir := filepath.Join(filepath.Dir(d.worktreeBase), "sessions")
if err := os.MkdirAll(sessDir, 0o755); err != nil {
	return fmt.Errorf("create sessions dir: %w", err)
}
```

- [ ] **Step 3: Update `cmd/dispatchd/main.go` to set `SessionDir`**

```go
spawner := &daemon.ClaudeSpawner{
	ClaudeBin:      "claude",
	WorkerPrompt:   "", // TODO: load in Task 5
	ReviewerPrompt: "", // TODO: load in Task 5
	OutputLines:    100,
	SessionDir:     filepath.Join(home, ".dispatch", "sessions"),
}
```

- [ ] **Step 4: Run tests**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./...`
Expected: All tests pass. (Tests don't set `SessionDir`, so logging is skipped via nil check.)

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/claude_spawner.go internal/daemon/daemon.go cmd/dispatchd/main.go
git commit -m "feat: wire session logging to disk via TeeWriter in ClaudeSpawner"
```

---

### Task 4: Add review gate logic to daemon

This is the core task — modifying `monitorWorkers` and `spawnReady` to support the review cycle.

**Files:**
- Modify: `internal/daemon/daemon.go`
- Create: `tests/review_gate_test.go`

- [ ] **Step 1: Add review tracking state to `Daemon` struct**

In `daemon.go`, add to the `Daemon` struct:

```go
type Daemon struct {
	// ... existing fields ...
	taskRoles      map[string]SpawnRole // taskID -> current role
	reviewRound    map[string]int       // taskID -> review round count
	noteCountAtReviewStart map[string]int // taskID -> note count when reviewer was spawned
}
```

In `New()`, initialize:

```go
taskRoles:      make(map[string]SpawnRole),
reviewRound:    make(map[string]int),
noteCountAtReviewStart: make(map[string]int),
```

- [ ] **Step 2: Refactor `spawnReady` — skip existing worktrees, set role, set log suffix, recover review round**

This is the full `spawnReady` refactoring. Consolidates worktree-exists check, role tracking, log suffix, and review round recovery into one step.

In `spawnReady`, refactor the per-task loop body. After claiming the task and determining the base branch:

```go
wtDir := filepath.Join(d.worktreeBase, task.ID)
branchName := fmt.Sprintf("dispatch/%s", task.ID)

// Check if worktree already exists (e.g., task reopened after review rejection).
if _, statErr := os.Stat(wtDir); statErr != nil {
	// Worktree doesn't exist — create it.
	// ... existing parent branch detection + CreateWorktree logic stays here ...
}

// Recover review round from existing log files (handles daemon restart).
if _, ok := d.reviewRound[task.ID]; !ok {
	d.reviewRound[task.ID] = recoverReviewRound(d.cfg.SessionDir, task.ID)
}

// Set log suffix for session logging.
round := d.reviewRound[task.ID]
if cs, ok := d.spawner.(*ClaudeSpawner); ok {
	if round == 0 {
		cs.LogSuffix = ""
	} else {
		cs.LogSuffix = fmt.Sprintf("-%d", round+1)
	}
}

// Spawn worker.
ctx := context.Background()
handle, err := d.spawner.Spawn(ctx, task, wtDir, RoleWorker)
// ... existing error handling (release task, remove worktree) ...

// Write PID file.
pidPath := filepath.Join(wtDir, "worker.pid")
if err := os.WriteFile(pidPath, []byte(strconv.Itoa(handle.PID())), 0o644); err != nil {
	d.logger.Printf("spawn: write PID file %s: %v", task.ID, err)
}

d.workers[task.ID] = handle
d.taskRoles[task.ID] = RoleWorker
```

Add a helper function for review round recovery:

```go
// recoverReviewRound globs session log files to determine the current review round.
// Returns 0 if no review logs exist.
func recoverReviewRound(sessionDir, taskID string) int {
	if sessionDir == "" {
		return 0
	}
	pattern := filepath.Join(sessionDir, taskID+"-review-*.log")
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return 0
	}
	return len(matches)
}
```

Add `SessionDir` to the `Config` struct:

```go
type Config struct {
	// ... existing fields ...
	SessionDir string // path to ~/.dispatch/sessions/
}
```

- [ ] **Step 3: Write tests for review gate flow**

Create `tests/review_gate_test.go`:

```go
package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
)

// reviewSpawner simulates a worker that exits 0, then a reviewer that approves.
type reviewSpawner struct {
	db         *db.DB
	spawnCount map[string]int // track spawns per task
}

func newReviewSpawner(database *db.DB) *reviewSpawner {
	return &reviewSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *reviewSpawner) Spawn(_ context.Context, task db.Task, _ string, role daemon.SpawnRole) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	done := make(chan struct{})
	close(done)
	return &immediateHandle{done: done}, nil
}

func TestDaemonIntegration_ReviewGateApproval(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("review test", "test review gate", "", "")
	if err != nil {
		t.Fatal(err)
	}

	spawner := newReviewSpawner(database)

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   4,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	// Task should go through worker -> reviewer -> done.
	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "task done after review", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	cancel()
	<-doneCh

	// Should have been spawned exactly 2 times (worker + reviewer).
	if count := spawner.spawnCount[task.ID]; count != 2 {
		t.Errorf("spawn count = %d, want 2 (worker + reviewer)", count)
	}
}

// rejectingReviewSpawner: worker exits 0, reviewer rejects once (adds note, exits non-zero),
// then worker exits 0 again, reviewer approves.
type rejectingReviewSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newRejectingReviewSpawner(database *db.DB) *rejectingReviewSpawner {
	return &rejectingReviewSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *rejectingReviewSpawner) Spawn(_ context.Context, task db.Task, _ string, role daemon.SpawnRole) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	count := s.spawnCount[task.ID]

	done := make(chan struct{})

	if role == daemon.RoleReviewer && count == 2 {
		// First review — reject with feedback note.
		author := "reviewer"
		s.db.AddNote(task.ID, "Review round 1 — REJECTED\n\nIssues:\n- Missing tests", &author)
		close(done)
		return &failHandle{done: done}, nil
	}

	// All other spawns (workers, second reviewer) — exit 0.
	close(done)
	return &immediateHandle{done: done}, nil
}

type failHandle struct {
	done chan struct{}
}

func (h *failHandle) PID() int             { return os.Getpid() }
func (h *failHandle) Wait() error          { <-h.done; return fmt.Errorf("exit code 1") }
func (h *failHandle) Done() <-chan struct{} { return h.done }
func (h *failHandle) Err() error           { return fmt.Errorf("exit code 1") }
func (h *failHandle) Output() string       { return "review failed" }

func TestDaemonIntegration_ReviewGateRejectionAndRetry(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("rejection test", "test review rejection", "", "")
	if err != nil {
		t.Fatal(err)
	}

	spawner := newRejectingReviewSpawner(database)

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   4,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	// Task should eventually complete after: worker1 -> reviewer1(reject) -> worker2 -> reviewer2(approve).
	waitForCondition(t, 8*time.Second, 100*time.Millisecond, "task done after retry", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	cancel()
	<-doneCh

	// 4 spawns: worker, reviewer(reject), worker, reviewer(approve).
	if count := spawner.spawnCount[task.ID]; count != 4 {
		t.Errorf("spawn count = %d, want 4", count)
	}

	// Verify review feedback note exists.
	notes, err := database.GetNotes(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReview := false
	for _, n := range notes {
		if n.Author != nil && *n.Author == "reviewer" {
			foundReview = true
		}
	}
	if !foundReview {
		t.Error("expected reviewer note to be present")
	}
}

// crashingReviewerSpawner: worker exits 0, reviewer crashes (exits non-zero, no notes).
type crashingReviewerSpawner struct {
	spawnCount map[string]int
}

func newCrashingReviewerSpawner() *crashingReviewerSpawner {
	return &crashingReviewerSpawner{spawnCount: make(map[string]int)}
}

func (s *crashingReviewerSpawner) Spawn(_ context.Context, task db.Task, _ string, role daemon.SpawnRole) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	done := make(chan struct{})

	if role == daemon.RoleReviewer {
		// Crash — no notes added, just exit non-zero.
		close(done)
		return &failHandle{done: done}, nil
	}

	close(done)
	return &immediateHandle{done: done}, nil
}

func TestDaemonIntegration_ReviewerCrashBlocksTask(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("reviewer crash test", "test reviewer crash", "", "")
	if err != nil {
		t.Fatal(err)
	}

	spawner := newCrashingReviewerSpawner()

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   4,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	// Task should be blocked (reviewer crashed, no notes).
	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "task blocked after reviewer crash", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "blocked"
	})

	cancel()
	<-doneCh

	updated, _ := database.GetTask(task.ID)
	if updated.BlockReason == nil || *updated.BlockReason == "" {
		t.Error("expected block reason for reviewer crash")
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./tests/ -run TestDaemonIntegration_ReviewGate -v`
Expected: FAIL — review gate logic not implemented yet.

- [ ] **Step 5: Implement review gate in `monitorWorkers`**

In `daemon.go`, replace the clean-exit handling in `monitorWorkers`. The current block (lines 267-314) handles clean exits. Replace it with logic that checks the role:

**Important:** Preserve the existing adopted-process check from the current `monitorWorkers`. When a worker reports an error, check if the task is already `done` (worker called `dt done` before exiting) before blocking. This prevents incorrectly blocking workers that completed successfully while the daemon was down.

```go
for _, f := range finished {
	handle := d.workers[f.taskID]
	role := d.taskRoles[f.taskID]
	delete(d.workers, f.taskID)
	delete(d.taskRoles, f.taskID)

	// Preserve adopted-process check: if the worker reported an error but the task
	// is already done (worker called dt done before exiting), treat as clean exit.
	waitErr := f.err
	if waitErr != nil {
		if task, err := d.db.GetTask(f.taskID); err == nil && task.Status == "done" {
			waitErr = nil
		}
	}

	if waitErr == nil {
		// Clean exit.
		if role == RoleReviewer {
			d.handleReviewApproval(f.taskID)
		} else {
			d.handleWorkerComplete(f.taskID)
		}
	} else {
		// Non-zero exit.
		if role == RoleReviewer {
			d.handleReviewerExit(f.taskID, handle)
		} else {
			d.handleWorkerCrash(f.taskID, waitErr, handle)
		}
	}
}
```

Add helper methods:

```go
// handleWorkerComplete spawns a reviewer in the same worktree after a worker exits 0.
func (d *Daemon) handleWorkerComplete(taskID string) {
	wtDir := filepath.Join(d.worktreeBase, taskID)

	task, err := d.db.GetTask(taskID)
	if err != nil {
		d.logger.Printf("review: get task %s: %v", taskID, err)
		return
	}

	// Record note count before reviewer spawns — used to detect intentional rejection vs crash.
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("review: get notes %s: %v", taskID, err)
		notes = nil
	}
	d.noteCountAtReviewStart[taskID] = len(notes)

	// Set log suffix for session logging.
	round := d.reviewRound[taskID] + 1 // rounds are 1-indexed
	if cs, ok := d.spawner.(*ClaudeSpawner); ok {
		cs.LogSuffix = fmt.Sprintf("-review-%d", round)
	}

	ctx := context.Background()
	handle, err := d.spawner.Spawn(ctx, *task, wtDir, RoleReviewer)
	if err != nil {
		d.logger.Printf("review: spawn reviewer %s: %v", taskID, err)
		if _, err := d.db.BlockTask(taskID, fmt.Sprintf("failed to spawn reviewer: %v", err)); err != nil {
			d.logger.Printf("review: block task %s: %v", taskID, err)
		}
		return
	}

	// Write PID file for reviewer (overwrites worker PID).
	pidPath := filepath.Join(wtDir, "worker.pid")
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(handle.PID())), 0o644); err != nil {
		d.logger.Printf("review: write PID file %s: %v", taskID, err)
	}

	d.workers[taskID] = handle
	d.taskRoles[taskID] = RoleReviewer
	d.logger.Printf("spawned reviewer for task %s (pid %d)", taskID, handle.PID())
}

// handleReviewApproval merges the branch and marks the task done.
func (d *Daemon) handleReviewApproval(taskID string) {
	task, err := d.db.GetTask(taskID)
	if err != nil {
		d.logger.Printf("review-done: get task %s: %v", taskID, err)
		return
	}

	wtDir := filepath.Join(d.worktreeBase, taskID)
	branchName := fmt.Sprintf("dispatch/%s", taskID)

	if task.ParentID != nil {
		parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
		if err := MergeBranch(d.repoPath, branchName, parentBranch); err != nil {
			d.logger.Printf("review-done: merge %s into %s failed: %v", branchName, parentBranch, err)
			reason := fmt.Sprintf("Merge conflict merging into plan branch:\n%v", err)
			if _, err := d.db.BlockTask(taskID, reason); err != nil {
				d.logger.Printf("review-done: block task %s: %v", taskID, err)
			}
			return
		}
		if task.Status != "done" {
			if _, err := d.db.DoneTask(taskID); err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			}
		}
		if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	} else {
		if task.Status != "done" {
			if _, err := d.db.DoneTask(taskID); err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			}
		}
		if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	}
	delete(d.reviewRound, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	d.logger.Printf("task %s completed (review approved)", taskID)
}

// handleReviewerExit handles a reviewer that exited non-zero.
// Compares current note count to the count recorded when the reviewer was spawned.
// If new notes were added, treat as intentional rejection. Otherwise, treat as crash.
func (d *Daemon) handleReviewerExit(taskID string, handle WorkerHandle) {
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("review-exit: get notes %s: %v", taskID, err)
	}

	// Compare note count: if more notes exist now than when the reviewer spawned,
	// the reviewer added feedback (intentional rejection).
	startCount := d.noteCountAtReviewStart[taskID]
	newNotes := len(notes) > startCount

	if newNotes {
		// Intentional rejection — reopen for retry.
		if _, err := d.db.ReopenTask(taskID); err != nil {
			d.logger.Printf("review-reject: reopen task %s: %v", taskID, err)
			return
		}
		d.reviewRound[taskID]++
		delete(d.noteCountAtReviewStart, taskID)
		d.logger.Printf("task %s review rejected (round %d), reopening", taskID, d.reviewRound[taskID])
	} else {
		// Reviewer crashed — block.
		wtDir := filepath.Join(d.worktreeBase, taskID)
		branchName := fmt.Sprintf("dispatch/%s", taskID)
		output := handle.Output()
		reason := fmt.Sprintf("Reviewer crashed: exit non-zero without feedback\n\nLast output:\n%s", output)
		if len(reason) > 4000 {
			reason = reason[:4000]
		}
		if _, err := d.db.BlockTask(taskID, reason); err != nil {
			d.logger.Printf("review-crash: block task %s: %v", taskID, err)
		}
		if err := RemoveWorktree(d.repoPath, wtDir, branchName, false); err != nil {
			d.logger.Printf("review-crash: cleanup worktree %s: %v", taskID, err)
		}
		delete(d.reviewRound, taskID)
		delete(d.noteCountAtReviewStart, taskID)
		d.logger.Printf("task %s blocked: reviewer crashed", taskID)
	}
}

// handleWorkerCrash blocks a crashed worker with log context (existing behavior).
func (d *Daemon) handleWorkerCrash(taskID string, exitErr error, handle WorkerHandle) {
	wtDir := filepath.Join(d.worktreeBase, taskID)
	branchName := fmt.Sprintf("dispatch/%s", taskID)
	output := handle.Output()
	reason := fmt.Sprintf("Worker exited: %v\n\nLast output:\n%s", exitErr, output)
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
	if _, err := d.db.BlockTask(taskID, reason); err != nil {
		d.logger.Printf("monitor: block task %s: %v", taskID, err)
	}
	if err := RemoveWorktree(d.repoPath, wtDir, branchName, false); err != nil {
		d.logger.Printf("monitor: cleanup worktree %s: %v", taskID, err)
	}
	delete(d.reviewRound, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	d.logger.Printf("task %s blocked: %v", taskID, exitErr)
}
```

The `LogSuffix` setting for worker spawns is handled in `spawnReady` (Step 2). The `LogSuffix` setting for reviewer spawns is in `handleWorkerComplete` above.

- [ ] **Step 6: Run tests**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./tests/ -run TestDaemonIntegration_ReviewGate -v`
Expected: All three review gate tests pass.

Run: `cd /home/tal/Documents/dispatch-ai && go test ./...`
Expected: All tests pass (including existing tests).

- [ ] **Step 7: Commit**

```bash
git add internal/daemon/daemon.go tests/review_gate_test.go
git commit -m "feat: add review gate to daemon — reviewer spawns after worker completion"
```

---

### Task 5: Create prompt files and load them in `dispatchd`

**Files:**
- Create: `prompts/worker.md`
- Create: `prompts/reviewer.md`
- Modify: `cmd/dispatchd/main.go`

- [ ] **Step 1: Create `prompts/worker.md`**

Copy the worker prompt content from the spec (Section 3). Use `$TASK_ID` as the placeholder — the daemon substitutes it at spawn time.

- [ ] **Step 2: Create `prompts/reviewer.md`**

Copy the reviewer prompt content from the spec (Section 4). Use `$TASK_ID` as the placeholder.

- [ ] **Step 3: Add prompt loading flags to `cmd/dispatchd/main.go`**

Add flags for prompt file paths:

```go
rootCmd.Flags().String("worker-prompt", envOrDefault("DISPATCH_WORKER_PROMPT", ""), "path to worker.md prompt file (required)")
rootCmd.Flags().String("reviewer-prompt", envOrDefault("DISPATCH_REVIEWER_PROMPT", ""), "path to reviewer.md prompt file (required)")
```

In the `RunE` function, load the prompt files:

```go
workerPromptPath, _ := cmd.Flags().GetString("worker-prompt")
reviewerPromptPath, _ := cmd.Flags().GetString("reviewer-prompt")

if workerPromptPath == "" || reviewerPromptPath == "" {
	return fmt.Errorf("--worker-prompt and --reviewer-prompt are required")
}

workerPrompt, err := os.ReadFile(workerPromptPath)
if err != nil {
	return fmt.Errorf("read worker prompt: %w", err)
}
reviewerPrompt, err := os.ReadFile(reviewerPromptPath)
if err != nil {
	return fmt.Errorf("read reviewer prompt: %w", err)
}
```

Set them on the spawner:

```go
spawner := &daemon.ClaudeSpawner{
	ClaudeBin:      "claude",
	WorkerPrompt:   string(workerPrompt),
	ReviewerPrompt: string(reviewerPrompt),
	OutputLines:    100,
	SessionDir:     filepath.Join(home, ".dispatch", "sessions"),
}
```

- [ ] **Step 4: Run build to verify compilation**

Run: `cd /home/tal/Documents/dispatch-ai && go build ./cmd/dispatchd/`
Expected: Compiles without errors.

- [ ] **Step 5: Commit**

```bash
git add prompts/worker.md prompts/reviewer.md cmd/dispatchd/main.go
git commit -m "feat: add worker and reviewer prompts, load them in dispatchd"
```

---

### Task 6: Update dispatch-planner skill sizing guidance

**Files:**
- Modify: `.claude/skills/dispatch-planner.md`

- [ ] **Step 1: Update sizing guidance**

In `.claude/skills/dispatch-planner.md`, replace the "Sizing Guidance" section:

Current:
```markdown
### Sizing Guidance

Size by **scope coherence** (one logical area) and **decision density** (non-obvious choices the worker must make).

- Boilerplate and tests are cheap. Novel logic, API design, error handling are expensive.
- Heuristic: >10 files or >300 lines of non-trivial logic means consider splitting.
- The test: can a worker hold the full context and execute reliably without going off the rails?
```

New:
```markdown
### Sizing Guidance

A task should be one atomic idea — a single coherent change you can explain in one sentence. The test is not file count or line count but decision count: if a worker needs to make two independent decisions, it's two tasks.

Split when:
- The task requires unrelated changes that don't inform each other.
- A natural description uses "and" to connect two independent actions.
- A reviewer would need to evaluate two separate concerns.

Don't split when:
- Multiple files change but they're all part of the same logical change.
- Tests and implementation are for the same feature.
- The changes only make sense together.
```

- [ ] **Step 2: Commit**

```bash
git add .claude/skills/dispatch-planner.md
git commit -m "docs: update dispatch-planner sizing guidance to atomic idea model"
```

---

### Task 7: Build binaries and verify

**Files:** None (build verification only)

- [ ] **Step 1: Build both binaries**

Run: `cd /home/tal/Documents/dispatch-ai && go build -o dt ./cmd/dt/ && go build -o dispatchd ./cmd/dispatchd/`
Expected: Both compile.

- [ ] **Step 2: Run full test suite**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./... -v`
Expected: All tests pass.

- [ ] **Step 3: Commit built binary to .gitignore if not already**

Check if `dt` and `dispatchd` are in `.gitignore`. If not, add them.

```bash
git add .gitignore
git commit -m "chore: add dispatchd to .gitignore"
```
