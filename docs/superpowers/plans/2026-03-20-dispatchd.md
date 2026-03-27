# `dispatchd` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the dispatch daemon that polls for ready tasks, spawns Claude Code workers in git worktrees, monitors their lifecycle, and handles exits.

**Architecture:** A single-binary daemon (`cmd/dispatchd`) imports `internal/db` directly. The main loop polls every N seconds, spawning workers via a `WorkerSpawner` interface (default: Claude CLI). Process state is in-memory with PID files for restart recovery. Git worktrees provide isolation per task.

**Tech Stack:** Go, SQLite via `internal/db`, `os/exec` for process management, cobra for CLI, no new external dependencies.

**Spec:** `docs/superpowers/specs/2026-03-20-dispatchd-design.md`

---

## File Structure

```
internal/
  daemon/
    daemon.go        — Daemon struct, main loop, startup recovery, shutdown
    daemon_test.go   — Unit tests for daemon logic
    worker.go        — WorkerSpawner/WorkerHandle interfaces + ClaudeSpawner
    worker_test.go   — Tests for ClaudeSpawner (with mock process)
    worktree.go      — Git worktree create/teardown/detect-default-branch
    worktree_test.go — Worktree management tests (uses real git repos)
    ringbuf.go       — Ring buffer for capturing last N lines of output
    ringbuf_test.go  — Ring buffer tests
cmd/
  dispatchd/
    main.go          — cobra root command, flags, env var fallbacks, run daemon
```

**Why this split:**
- `daemon.go` owns the orchestration loop and state — the core logic.
- `worker.go` owns the spawner interface and Claude implementation — swappable.
- `worktree.go` owns git operations — testable with real git repos in tmp dirs.
- `ringbuf.go` is a small utility — pure, no dependencies, trivially testable.
- All in `internal/daemon` because nothing outside the daemon binary needs these.

---

## Task 1: Ring Buffer

A line-oriented ring buffer that captures the last N lines written to it. Used to grab crash context from worker output.

**Files:**
- Create: `internal/daemon/ringbuf.go`
- Create: `internal/daemon/ringbuf_test.go`

- [ ] **Step 1: Write failing tests for ring buffer**

```go
// internal/daemon/ringbuf_test.go
package daemon

import (
	"fmt"
	"testing"
)

func TestRingBuf_UnderCapacity(t *testing.T) {
	rb := NewRingBuf(5)
	rb.Write([]byte("line1\nline2\nline3\n"))
	got := rb.String()
	want := "line1\nline2\nline3"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_OverCapacity(t *testing.T) {
	rb := NewRingBuf(3)
	for i := 1; i <= 5; i++ {
		rb.Write([]byte(fmt.Sprintf("line%d\n", i)))
	}
	got := rb.String()
	want := "line3\nline4\nline5"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_Empty(t *testing.T) {
	rb := NewRingBuf(5)
	if got := rb.String(); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestRingBuf_PartialLines(t *testing.T) {
	rb := NewRingBuf(3)
	rb.Write([]byte("partial"))
	rb.Write([]byte(" still partial\nline2\n"))
	got := rb.String()
	want := "partial still partial\nline2"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBuf_ImplementsWriter(t *testing.T) {
	var _ interface{ Write([]byte) (int, error) } = NewRingBuf(5)
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestRingBuf`
Expected: FAIL — package doesn't exist yet.

- [ ] **Step 3: Implement ring buffer**

```go
// internal/daemon/ringbuf.go
package daemon

import (
	"io"
	"strings"
	"sync"
)

// Compile-time check that RingBuf implements io.Writer.
var _ io.Writer = (*RingBuf)(nil)

// RingBuf is a line-oriented ring buffer that keeps the last N lines.
// It implements io.Writer.
type RingBuf struct {
	mu      sync.Mutex
	lines   []string
	maxLines int
	partial string // incomplete line (no trailing newline yet)
}

// NewRingBuf creates a ring buffer that keeps the last n lines.
func NewRingBuf(n int) *RingBuf {
	return &RingBuf{
		lines:    make([]string, 0, n),
		maxLines: n,
	}
}

// Write implements io.Writer. It splits input on newlines and stores
// complete lines in the ring buffer.
func (r *RingBuf) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s := r.partial + string(p)
	parts := strings.Split(s, "\n")

	// Last element is either empty (input ended with \n) or a partial line.
	r.partial = parts[len(parts)-1]
	completedLines := parts[:len(parts)-1]

	for _, line := range completedLines {
		r.addLine(line)
	}
	return len(p), nil
}

func (r *RingBuf) addLine(line string) {
	if len(r.lines) >= r.maxLines {
		// Shift left by 1.
		copy(r.lines, r.lines[1:])
		r.lines[len(r.lines)-1] = line
	} else {
		r.lines = append(r.lines, line)
	}
}

// String returns all stored lines joined by newlines.
// Includes any partial (unterminated) line at the end.
func (r *RingBuf) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	result := make([]string, len(r.lines))
	copy(result, r.lines)
	if r.partial != "" {
		result = append(result, r.partial)
	}
	return strings.Join(result, "\n")
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestRingBuf`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/ringbuf.go internal/daemon/ringbuf_test.go
git commit -m "feat(daemon): add ring buffer for capturing worker output tail"
```

---

## Task 2: Git Worktree Management

Functions to create worktrees, tear them down, and detect the default branch. All git operations run relative to a configurable repo path.

**Files:**
- Create: `internal/daemon/worktree.go`
- Create: `internal/daemon/worktree_test.go`

- [ ] **Step 1: Write failing tests**

Tests use real git repos in temp directories — no mocking git.

```go
// internal/daemon/worktree_test.go
package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a bare-minimum git repo with one commit.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestDetectDefaultBranch(t *testing.T) {
	repo := initTestRepo(t)
	branch, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	// git init creates "master" or "main" depending on config.
	if branch != "main" && branch != "master" {
		t.Errorf("unexpected default branch: %s", branch)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-test")

	err := CreateWorktree(repo, wtDir, "dispatch/test-task", "")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the worktree directory exists.
	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	// Verify we're on the expected branch.
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = wtDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "dispatch/test-task" {
		t.Errorf("worktree branch = %q, want dispatch/test-task", got)
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-remove")

	CreateWorktree(repo, wtDir, "dispatch/rm-task", "")

	err := RemoveWorktree(repo, wtDir, "dispatch/rm-task", true)
	if err != nil {
		t.Fatal(err)
	}

	// Verify directory gone.
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir still exists after removal")
	}

	// Verify branch deleted (deleteBranch=true).
	cmd := exec.Command("git", "branch", "--list", "dispatch/rm-task")
	cmd.Dir = repo
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Error("branch still exists after removal with deleteBranch=true")
	}
}

func TestRemoveWorktree_KeepBranch(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-keep")

	CreateWorktree(repo, wtDir, "dispatch/keep-task", "")

	err := RemoveWorktree(repo, wtDir, "dispatch/keep-task", false)
	if err != nil {
		t.Fatal(err)
	}

	// Verify worktree dir gone but branch kept.
	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir still exists")
	}
	cmd := exec.Command("git", "branch", "--list", "dispatch/keep-task")
	cmd.Dir = repo
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) == "" {
		t.Error("branch was deleted when it should have been kept")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDetect\|TestCreate\|TestRemove`
Expected: FAIL — functions don't exist.

- [ ] **Step 3: Implement worktree management**

```go
// internal/daemon/worktree.go
package daemon

import (
	"fmt"
	"os/exec"
	"strings"
)

// DetectDefaultBranch returns the default branch of the repo at repoDir.
// Tries `git symbolic-ref refs/remotes/origin/HEAD`, falls back to
// checking for main/master branches, then the current branch.
func DetectDefaultBranch(repoDir string) (string, error) {
	// Try origin HEAD first.
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		// refs/remotes/origin/main -> main
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1], nil
	}

	// Fallback: check current branch.
	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect default branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "main", nil // detached HEAD, guess main
	}
	return branch, nil
}

// CreateWorktree creates a git worktree at wtDir on a new branch branchName.
// If baseBranch is empty, the default branch is detected.
func CreateWorktree(repoDir, wtDir, branchName, baseBranch string) error {
	if baseBranch == "" {
		var err error
		baseBranch, err = DetectDefaultBranch(repoDir)
		if err != nil {
			return err
		}
	}

	cmd := exec.Command("git", "worktree", "add", wtDir, "-b", branchName, baseBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create worktree: %w\n%s", err, out)
	}
	return nil
}

// RemoveWorktree removes the worktree at wtDir. If deleteBranch is true,
// also deletes the branch.
func RemoveWorktree(repoDir, wtDir, branchName string, deleteBranch bool) error {
	cmd := exec.Command("git", "worktree", "remove", wtDir, "--force")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove worktree: %w\n%s", err, out)
	}

	if deleteBranch {
		cmd = exec.Command("git", "branch", "-D", branchName)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("delete branch %s: %w\n%s", branchName, err, out)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDetect\|TestCreate\|TestRemove`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/worktree.go internal/daemon/worktree_test.go
git commit -m "feat(daemon): add git worktree create/remove/detect-default-branch"
```

---

## Task 3: Worker Spawner Interface + Mock

Define the `WorkerSpawner` and `WorkerHandle` interfaces, plus a mock implementation for testing the daemon loop without Claude.

**Files:**
- Create: `internal/daemon/worker.go`
- Create: `internal/daemon/worker_test.go`

- [ ] **Step 1: Write failing test for mock spawner**

```go
// internal/daemon/worker_test.go
package daemon

import (
	"context"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestMockSpawner_Success(t *testing.T) {
	spawner := &MockSpawner{ExitCode: 0}
	handle, err := spawner.Spawn(context.Background(), db.Task{ID: "test"}, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if handle.PID() <= 0 {
		t.Error("expected positive PID")
	}
	if err := handle.Wait(); err != nil {
		t.Errorf("expected nil error for exit code 0, got: %v", err)
	}
}

func TestMockSpawner_Failure(t *testing.T) {
	spawner := &MockSpawner{ExitCode: 1, OutputText: "something went wrong"}
	handle, err := spawner.Spawn(context.Background(), db.Task{ID: "test"}, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(); err == nil {
		t.Error("expected error for exit code 1")
	}
	if got := handle.Output(); got != "something went wrong" {
		t.Errorf("output = %q, want %q", got, "something went wrong")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestMockSpawner`
Expected: FAIL

- [ ] **Step 3: Implement interfaces and mock**

```go
// internal/daemon/worker.go
package daemon

import (
	"context"
	"fmt"
	"os"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// WorkerSpawner starts worker processes for tasks.
type WorkerSpawner interface {
	Spawn(ctx context.Context, task db.Task, workDir string) (WorkerHandle, error)
}

// WorkerHandle monitors a running worker process.
type WorkerHandle interface {
	// PID returns the OS process ID.
	PID() int
	// Wait blocks until the process exits and returns the exit error (nil for code 0).
	Wait() error
	// Done returns a channel that is closed when the process has exited.
	// monitorWorkers uses this for non-blocking exit detection instead of
	// polling isProcessAlive, which doesn't work for mock handles.
	Done() <-chan struct{}
	// Err returns the exit error after Done() is closed. Returns nil before exit.
	Err() error
	// Output returns the captured stdout/stderr tail (last 100 lines).
	Output() string
}

// MockSpawner is a test double that simulates worker processes.
type MockSpawner struct {
	ExitCode   int
	OutputText string
	SpawnErr   error
	Spawned    []db.Task // tracks what was spawned
}

func (m *MockSpawner) Spawn(_ context.Context, task db.Task, _ string) (WorkerHandle, error) {
	if m.SpawnErr != nil {
		return nil, m.SpawnErr
	}
	m.Spawned = append(m.Spawned, task)
	h := &mockHandle{
		pid:      os.Getpid(),
		exitCode: m.ExitCode,
		output:   m.OutputText,
		done:     make(chan struct{}),
	}
	// Mock workers exit immediately.
	if m.ExitCode != 0 {
		h.exitErr = fmt.Errorf("exit code %d", m.ExitCode)
	}
	close(h.done)
	return h, nil
}

type mockHandle struct {
	pid      int
	exitCode int
	exitErr  error
	output   string
	done     chan struct{}
}

func (h *mockHandle) PID() int            { return h.pid }
func (h *mockHandle) Done() <-chan struct{} { return h.done }
func (h *mockHandle) Err() error          { return h.exitErr }
func (h *mockHandle) Output() string      { return h.output }

func (h *mockHandle) Wait() error {
	<-h.done
	return h.exitErr
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestMockSpawner`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/worker.go internal/daemon/worker_test.go
git commit -m "feat(daemon): add WorkerSpawner interface and mock implementation"
```

---

## Task 4: Claude Spawner

The real `WorkerSpawner` implementation that shells out to the `claude` CLI.

**Files:**
- Create: `internal/daemon/claude_spawner.go`
- Create: `internal/daemon/claude_spawner_test.go`

- [ ] **Step 1: Write failing test**

Test that `ClaudeSpawner` builds the right command and captures output. Uses a test helper script instead of the real `claude` binary.

```go
// internal/daemon/claude_spawner_test.go
package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestClaudeSpawner_BuildsCommand(t *testing.T) {
	// Create a fake "claude" script that just echoes and exits.
	tmpDir := t.TempDir()
	fakeClaude := filepath.Join(tmpDir, "claude")
	err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho \"working on task\"\nexit 0\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &ClaudeSpawner{
		ClaudeBin:    fakeClaude,
		SystemPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc1", Title: "Test task", Description: "Do the thing"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Wait(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if out := handle.Output(); out == "" {
		t.Error("expected some output from fake claude")
	}
}

func TestClaudeSpawner_NonZeroExit(t *testing.T) {
	tmpDir := t.TempDir()
	fakeClaude := filepath.Join(tmpDir, "claude")
	err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho \"error output\" >&2\nexit 1\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &ClaudeSpawner{
		ClaudeBin:    fakeClaude,
		SystemPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc2", Title: "Failing task"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Wait(); err == nil {
		t.Error("expected error for non-zero exit")
	}

	if out := handle.Output(); out == "" {
		t.Error("expected captured error output")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestClaudeSpawner`
Expected: FAIL

- [ ] **Step 3: Implement ClaudeSpawner**

```go
// internal/daemon/claude_spawner.go
package daemon

import (
	"context"
	"fmt"
	"io"
	"os/exec"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// ClaudeSpawner spawns Claude Code CLI processes as workers.
type ClaudeSpawner struct {
	ClaudeBin    string // path to claude binary, default "claude"
	SystemPrompt string // contents of worker.md
	OutputLines  int    // ring buffer size, default 100
}

func (s *ClaudeSpawner) Spawn(ctx context.Context, task db.Task, workDir string) (WorkerHandle, error) {
	bin := s.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	lines := s.OutputLines
	if lines == 0 {
		lines = 100
	}

	prompt := fmt.Sprintf("Your task ID is %s. Run `dt show %s` to read your assignment.", task.ID, task.ID)

	args := []string{
		"--print",
		"--system-prompt", s.SystemPrompt,
		"--prompt", prompt,
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir

	buf := NewRingBuf(lines)
	cmd.Stdout = buf
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	h := &claudeHandle{cmd: cmd, buf: buf, done: make(chan struct{})}
	// Start a goroutine that waits for the process and signals done.
	go func() {
		h.exitErr = cmd.Wait()
		close(h.done)
	}()

	return h, nil
}

type claudeHandle struct {
	cmd     *exec.Cmd
	buf     *RingBuf
	done    chan struct{}
	exitErr error
}

func (h *claudeHandle) PID() int            { return h.cmd.Process.Pid }
func (h *claudeHandle) Done() <-chan struct{} { return h.done }
func (h *claudeHandle) Err() error          { <-h.done; return h.exitErr }
func (h *claudeHandle) Wait() error         { return h.Err() }
func (h *claudeHandle) Output() string      { return h.buf.String() }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestClaudeSpawner`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/claude_spawner.go internal/daemon/claude_spawner_test.go
git commit -m "feat(daemon): add ClaudeSpawner that shells out to claude CLI"
```

---

## Task 5: Daemon Core — Struct, Config, Startup

The `Daemon` struct, configuration, and startup recovery logic.

**Files:**
- Create: `internal/daemon/daemon.go`
- Create: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write failing tests for config and startup recovery**

```go
// internal/daemon/daemon_test.go
package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// Note: initTestRepo is defined in worktree_test.go (same package).

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestDaemonConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MaxWorkers != 4 {
		t.Errorf("MaxWorkers = %d, want 4", cfg.MaxWorkers)
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
}

func TestDaemon_RecoverActive_DeadProcess(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	// Create a task and claim it.
	task, _ := d.AddTask("recover test", "", "", "")
	d.ClaimTask(task.ID, "old-session")

	// Create a fake worktree dir with a PID file pointing to a dead PID.
	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	// PID 99999999 is almost certainly not running.
	os.WriteFile(filepath.Join(wtDir, "worker.pid"), []byte("99999999"), 0o644)

	daemon := &Daemon{
		db:           d,
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
	}

	daemon.recoverActive()

	// Task should now be blocked.
	updated, _ := d.GetTask(task.ID)
	if updated.Status != "blocked" {
		t.Errorf("status = %s, want blocked", updated.Status)
	}
}

func TestDaemon_RecoverActive_NoWorktree(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("no worktree test", "", "", "")
	d.ClaimTask(task.ID, "old-session")

	daemon := &Daemon{
		db:           d,
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "blocked" {
		t.Errorf("status = %s, want blocked", updated.Status)
	}
}

func TestDaemon_RecoverActive_LiveProcess(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("live process test", "", "", "")
	d.ClaimTask(task.ID, "old-session")

	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	// Use our own PID — guaranteed to be alive.
	os.WriteFile(filepath.Join(wtDir, "worker.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)

	// We need a mock handle for re-adoption. The daemon can't re-adopt a real process
	// it didn't spawn — it creates a placeholder handle.
	daemon := &Daemon{
		db:           d,
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
	}

	daemon.recoverActive()

	// Task should still be active (re-adopted).
	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon`
Expected: FAIL

- [ ] **Step 3: Implement Daemon struct, config, and recovery**

```go
// internal/daemon/daemon.go
package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// Config holds daemon configuration.
type Config struct {
	DBPath       string
	MaxWorkers   int
	BaseBranch   string // empty = auto-detect
	RepoPath     string
	PollInterval time.Duration
	WorktreeBase string // default ~/.dispatch/worktrees
}

// DefaultConfig returns configuration with defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DBPath:       filepath.Join(home, ".dispatch", "dispatch.db"),
		MaxWorkers:   4,
		RepoPath:     ".",
		PollInterval: 5 * time.Second,
		WorktreeBase: filepath.Join(home, ".dispatch", "worktrees"),
	}
}

// Daemon orchestrates worker processes.
type Daemon struct {
	db           *db.DB
	cfg          Config
	spawner      WorkerSpawner
	worktreeBase string
	repoPath     string
	baseBranch   string
	workers      map[string]WorkerHandle // taskID -> handle
	logger       *log.Logger
}

// New creates a Daemon from the given config and spawner.
func New(database *db.DB, cfg Config, spawner WorkerSpawner) *Daemon {
	return &Daemon{
		db:           database,
		cfg:          cfg,
		spawner:      spawner,
		worktreeBase: cfg.WorktreeBase,
		repoPath:     cfg.RepoPath,
		baseBranch:   cfg.BaseBranch,
		workers:      make(map[string]WorkerHandle),
		logger:       log.New(os.Stderr, "[dispatchd] ", log.LstdFlags),
	}
}

// recoverActive checks all active tasks in the DB and reconciles with
// actual process state using PID files.
func (d *Daemon) recoverActive() {
	tasks, err := d.db.ListTasks("active", false)
	if err != nil {
		d.logger.Printf("recovery: list active tasks: %v", err)
		return
	}

	for _, task := range tasks {
		wtDir := filepath.Join(d.worktreeBase, task.ID)

		// Check if worktree exists.
		if _, err := os.Stat(wtDir); os.IsNotExist(err) {
			d.logger.Printf("recovery: task %s has no worktree, blocking", task.ID)
			d.db.BlockTask(task.ID, "unknown worker state after daemon restart")
			continue
		}

		// Read PID file.
		pidPath := filepath.Join(wtDir, "worker.pid")
		pidBytes, err := os.ReadFile(pidPath)
		if err != nil {
			d.logger.Printf("recovery: task %s has no PID file, blocking", task.ID)
			d.db.BlockTask(task.ID, "unknown worker state after daemon restart")
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			d.logger.Printf("recovery: task %s has invalid PID file, blocking", task.ID)
			d.db.BlockTask(task.ID, "invalid PID file after daemon restart")
			continue
		}

		// Check if process is alive.
		if isProcessAlive(pid) {
			d.logger.Printf("recovery: task %s has live worker (pid %d), re-adopting", task.ID, pid)
			// Create an adoptedHandle for monitoring. We can't get the real
			// exec.Cmd but we can monitor the PID.
			d.workers[task.ID] = newAdoptedHandle(pid)
		} else {
			d.logger.Printf("recovery: task %s worker (pid %d) is dead, blocking", task.ID, pid)
			d.db.BlockTask(task.ID, "worker died while daemon was down")
		}
	}
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks existence without actually sending a signal.
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// adoptedHandle monitors a process the daemon didn't spawn (re-adopted on restart).
// It polls isProcessAlive since we don't have the exec.Cmd.
type adoptedHandle struct {
	pid     int
	output  string
	done    chan struct{}
	exitErr error
}

func newAdoptedHandle(pid int) *adoptedHandle {
	h := &adoptedHandle{pid: pid, done: make(chan struct{})}
	go func() {
		// Poll until the process dies.
		for isProcessAlive(pid) {
			time.Sleep(1 * time.Second)
		}
		h.exitErr = fmt.Errorf("adopted process %d exited (status unknown)", pid)
		close(h.done)
	}()
	return h
}

func (h *adoptedHandle) PID() int            { return h.pid }
func (h *adoptedHandle) Done() <-chan struct{} { return h.done }
func (h *adoptedHandle) Err() error          { <-h.done; return h.exitErr }
func (h *adoptedHandle) Wait() error         { return h.Err() }
func (h *adoptedHandle) Output() string      { return h.output }
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): add Daemon struct with config and startup recovery"
```

---

## Task 6: Daemon Main Loop — Spawn + Monitor

The core poll-spawn-monitor loop. Claims tasks, creates worktrees, spawns workers, handles exits.

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write failing tests for the main loop**

Append to `daemon_test.go`:

```go
func TestDaemon_SpawnWorker(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	task, _ := d.AddTask("spawn test", "", "", "")

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		MaxWorkers:   4,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	// Run one spawn cycle.
	daemon.spawnReady()

	// Task should be claimed.
	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}

	// Spawner should have been called.
	if len(spawner.Spawned) != 1 {
		t.Fatalf("expected 1 spawn, got %d", len(spawner.Spawned))
	}
	if spawner.Spawned[0].ID != task.ID {
		t.Errorf("spawned task ID = %s, want %s", spawner.Spawned[0].ID, task.ID)
	}
}

func TestDaemon_MaxWorkers(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	// Create 5 tasks but max_workers = 2.
	for i := 0; i < 5; i++ {
		d.AddTask(fmt.Sprintf("task %d", i), "", "", "")
	}

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		MaxWorkers:   2,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	daemon.spawnReady()

	if len(spawner.Spawned) != 2 {
		t.Errorf("spawned %d tasks, want 2 (max_workers)", len(spawner.Spawned))
	}
}

func TestDaemon_MonitorCleanExit(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	task, _ := d.AddTask("monitor test", "", "", "")

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		MaxWorkers:   4,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	// Spawn, then simulate the worker calling dt done and exiting.
	daemon.spawnReady()

	// Manually mark task done (simulating worker calling dt done).
	d.DoneTask(task.ID)

	// Now monitor should detect the exit and clean up.
	// The mock handle's Wait() returns immediately with nil.
	daemon.monitorWorkers()

	// Worker should be removed from the map.
	if _, exists := daemon.workers[task.ID]; exists {
		t.Error("worker still in map after clean exit")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon_Spawn\|TestDaemon_Max\|TestDaemon_Monitor`
Expected: FAIL

- [ ] **Step 3: Implement spawnReady and monitorWorkers**

Add to `daemon.go`:

```go
// spawnReady polls for ready tasks and spawns workers up to MaxWorkers.
func (d *Daemon) spawnReady() {
	// Count current active workers.
	activeCount := len(d.workers)
	available := d.cfg.MaxWorkers - activeCount
	if available <= 0 {
		return
	}

	tasks, err := d.db.ReadyTasks()
	if err != nil {
		d.logger.Printf("poll: ready tasks: %v", err)
		return
	}

	for _, task := range tasks {
		if available <= 0 {
			break
		}

		// Claim first to prevent double-spawn.
		sessionID := fmt.Sprintf("dispatchd-%s", task.ID)
		if _, err := d.db.ClaimTask(task.ID, sessionID); err != nil {
			d.logger.Printf("spawn: claim %s: %v (already claimed?)", task.ID, err)
			continue
		}

		// Create worktree.
		wtDir := filepath.Join(d.worktreeBase, task.ID)
		branchName := fmt.Sprintf("dispatch/%s", task.ID)
		if err := CreateWorktree(d.repoPath, wtDir, branchName, d.baseBranch); err != nil {
			d.logger.Printf("spawn: worktree %s: %v", task.ID, err)
			d.db.ReleaseTask(task.ID)
			continue
		}

		// Spawn worker.
		ctx := context.Background()
		handle, err := d.spawner.Spawn(ctx, task, wtDir)
		if err != nil {
			d.logger.Printf("spawn: worker %s: %v", task.ID, err)
			RemoveWorktree(d.repoPath, wtDir, branchName, true)
			d.db.ReleaseTask(task.ID)
			continue
		}

		// Write PID file.
		pidPath := filepath.Join(wtDir, "worker.pid")
		os.WriteFile(pidPath, []byte(strconv.Itoa(handle.PID())), 0o644)

		d.workers[task.ID] = handle
		d.logger.Printf("spawned worker for task %s (pid %d)", task.ID, handle.PID())
		available--
	}
}

// monitorWorkers checks each active worker using the Done() channel
// for non-blocking exit detection. This works for both real processes
// (claudeHandle) and test doubles (mockHandle).
func (d *Daemon) monitorWorkers() {
	var finished []struct {
		taskID string
		err    error
	}

	for taskID, handle := range d.workers {
		// Non-blocking check: has the worker exited?
		select {
		case <-handle.Done():
			// Worker has exited.
		default:
			continue // Still running.
		}

		waitErr := handle.Err()
		finished = append(finished, struct {
			taskID string
			err    error
		}{taskID, waitErr})
	}

	for _, f := range finished {
		handle := d.workers[f.taskID]
		delete(d.workers, f.taskID)

		wtDir := filepath.Join(d.worktreeBase, f.taskID)
		branchName := fmt.Sprintf("dispatch/%s", f.taskID)

		if f.err == nil {
			// Clean exit. Check if worker already called dt done.
			task, err := d.db.GetTask(f.taskID)
			if err != nil {
				d.logger.Printf("monitor: get task %s: %v", f.taskID, err)
				continue
			}
			if task.Status != "done" {
				d.db.DoneTask(f.taskID)
			}
			// Clean up worktree and branch.
			if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
				d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
			}
			d.logger.Printf("task %s completed", f.taskID)
		} else {
			// Unclean exit. Block with log tail.
			output := handle.Output()
			reason := fmt.Sprintf("Worker exited: %v\n\nLast output:\n%s", f.err, output)
			if len(reason) > 4000 {
				reason = reason[:4000]
			}
			d.db.BlockTask(f.taskID, reason)
			// Remove worktree but keep branch for inspection.
			if err := RemoveWorktree(d.repoPath, wtDir, branchName, false); err != nil {
				d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
			}
			d.logger.Printf("task %s blocked: %v", f.taskID, f.err)
		}
	}
}
```

Also add the `cleanOrphanedWorktrees` method to `daemon.go`:

```go
// cleanOrphanedWorktrees removes worktree directories that don't correspond
// to any active task. This handles cases like daemon crash after spawn but
// before claim, or manual task state changes.
func (d *Daemon) cleanOrphanedWorktrees() {
	entries, err := os.ReadDir(d.worktreeBase)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		d.logger.Printf("cleanup: read worktree dir: %v", err)
		return
	}

	activeTasks, err := d.db.ListTasks("active", false)
	if err != nil {
		d.logger.Printf("cleanup: list active tasks: %v", err)
		return
	}
	activeIDs := make(map[string]bool, len(activeTasks))
	for _, t := range activeTasks {
		activeIDs[t.ID] = true
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !activeIDs[entry.Name()] {
			wtDir := filepath.Join(d.worktreeBase, entry.Name())
			branchName := fmt.Sprintf("dispatch/%s", entry.Name())
			d.logger.Printf("cleanup: removing orphaned worktree %s", entry.Name())
			RemoveWorktree(d.repoPath, wtDir, branchName, true)
		}
	}
}
```

Note: `context` import was already added to the initial `daemon.go` imports block in Task 5.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon_Spawn\|TestDaemon_Max\|TestDaemon_Monitor`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): add spawn and monitor loop logic"
```

---

## Task 7: Daemon Run Loop + Graceful Shutdown

Wire everything into a `Run` method with signal handling.

**Files:**
- Modify: `internal/daemon/daemon.go`
- Modify: `internal/daemon/daemon_test.go`

- [ ] **Step 1: Write failing test for run + shutdown**

Append to `daemon_test.go`:

```go
func TestDaemon_RunAndShutdown(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		MaxWorkers:   4,
		PollInterval: 50 * time.Millisecond, // fast for testing
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx)
	}()

	// Let it run a couple of cycles.
	time.Sleep(200 * time.Millisecond)

	// Signal shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down within 5 seconds")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon_RunAndShutdown`
Expected: FAIL — `Run` method doesn't exist.

- [ ] **Step 3: Implement Run with context-based shutdown**

Add to `daemon.go`:

```go
// Run starts the daemon main loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Println("starting daemon")

	// Recover any active tasks from a previous run.
	d.recoverActive()
	d.cleanOrphanedWorktrees()

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Println("shutting down...")
			d.shutdown()
			return ctx.Err()
		case <-ticker.C:
			d.spawnReady()
			d.monitorWorkers()
			d.logSummary()
		}
	}
}

// shutdown sends SIGTERM to all workers and waits for them to exit.
func (d *Daemon) shutdown() {
	if len(d.workers) == 0 {
		return
	}

	d.logger.Printf("sending SIGTERM to %d workers", len(d.workers))
	for taskID, handle := range d.workers {
		proc, err := os.FindProcess(handle.PID())
		if err == nil {
			proc.Signal(syscall.SIGTERM)
		}
		d.logger.Printf("sent SIGTERM to worker %s (pid %d)", taskID, handle.PID())
	}

	// Wait up to 30 seconds.
	deadline := time.After(30 * time.Second)
	for len(d.workers) > 0 {
		select {
		case <-deadline:
			d.logger.Printf("timeout: %d workers still running, exiting anyway", len(d.workers))
			return
		case <-time.After(500 * time.Millisecond):
			for taskID, handle := range d.workers {
				if !isProcessAlive(handle.PID()) {
					delete(d.workers, taskID)
					d.logger.Printf("worker %s exited during shutdown", taskID)
				}
			}
		}
	}
	d.logger.Println("all workers stopped")
}

// logSummary prints a brief status summary.
func (d *Daemon) logSummary() {
	if len(d.workers) > 0 {
		ids := make([]string, 0, len(d.workers))
		for id := range d.workers {
			ids = append(ids, id)
		}
		d.logger.Printf("active workers: %d [%s]", len(d.workers), strings.Join(ids, ", "))
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./internal/daemon/ -v -run TestDaemon_RunAndShutdown`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/daemon/daemon.go internal/daemon/daemon_test.go
git commit -m "feat(daemon): add Run loop with graceful shutdown"
```

---

## Task 8: CLI Entry Point (`cmd/dispatchd/main.go`)

The cobra-based binary with flags and env var fallbacks.

**Files:**
- Create: `cmd/dispatchd/main.go`

- [ ] **Step 1: Verify directory structure**

Run: `ls /home/tal/Documents/dispatch-ai/cmd/`
Expected: `dt` directory exists, `dispatchd` does not yet.

- [ ] **Step 2: Create the entry point**

```go
// cmd/dispatchd/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func defaultDBPath() string {
	if v := os.Getenv("DISPATCH_DB"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "dispatch.db"
	}
	return filepath.Join(home, ".dispatch", "dispatch.db")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

var rootCmd = &cobra.Command{
	Use:   "dispatchd",
	Short: "dispatch orchestration daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		maxWorkers, _ := cmd.Flags().GetInt("max-workers")
		baseBranch, _ := cmd.Flags().GetString("base-branch")
		repoPath, _ := cmd.Flags().GetString("repo")
		pollInterval, _ := cmd.Flags().GetDuration("poll-interval")

		database, err := db.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()

		home, _ := os.UserHomeDir()
		cfg := daemon.Config{
			DBPath:       dbPath,
			MaxWorkers:   maxWorkers,
			BaseBranch:   baseBranch,
			RepoPath:     repoPath,
			PollInterval: pollInterval,
			WorktreeBase: filepath.Join(home, ".dispatch", "worktrees"),
		}

		spawner := &daemon.ClaudeSpawner{
			ClaudeBin:   "claude",
			SystemPrompt: "", // TODO: load worker.md in Phase 3
			OutputLines: 100,
		}

		d := daemon.New(database, cfg, spawner)

		// Set up signal handling -> context cancellation.
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			fmt.Fprintf(os.Stderr, "\nreceived %s, shutting down...\n", sig)
			cancel()
		}()

		return d.Run(ctx)
	},
}

func init() {
	rootCmd.Flags().String("db", defaultDBPath(), "path to SQLite database")
	rootCmd.Flags().Int("max-workers", envIntOrDefault("DISPATCH_MAX_WORKERS", 4), "max concurrent workers")
	rootCmd.Flags().String("base-branch", envOrDefault("DISPATCH_BASE_BRANCH", ""), "base branch for worktrees (default: auto-detect)")
	rootCmd.Flags().String("repo", envOrDefault("DISPATCH_REPO", "."), "path to git repository")
	rootCmd.Flags().Duration("poll-interval", envDurationOrDefault("DISPATCH_POLL_INTERVAL", 5*time.Second), "poll interval")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `cd /home/tal/Documents/dispatch-ai && go build ./cmd/dispatchd/`
Expected: Successful build, no errors.

- [ ] **Step 4: Verify help output**

Run: `cd /home/tal/Documents/dispatch-ai && ./dispatchd --help`
Expected: Shows usage with all flags listed.

- [ ] **Step 5: Clean up and commit**

```bash
rm -f dispatchd
git add cmd/dispatchd/main.go
git commit -m "feat: add dispatchd binary with CLI flags and env var fallbacks"
```

---

## Task 9: Integration Test — Full Lifecycle

End-to-end test: create a task, start daemon with mock spawner, verify spawn → done → cleanup.

**Files:**
- Create: `tests/daemon_integration_test.go`

- [ ] **Step 1: Write integration test**

```go
// tests/daemon_integration_test.go
package tests

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
)

// initGitRepo creates a minimal git repo for worktree operations.
func initGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestDaemonIntegration_SpawnAndComplete(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create a task.
	task, err := database.AddTask("integration test task", "do something", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Use a mock spawner that simulates a worker calling dt done then exiting.
	spawner := &doneCallingSpawner{db: database}

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   4,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for the task to be completed.
	deadline := time.After(4 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("task was not completed within timeout")
		case <-time.After(100 * time.Millisecond):
			updated, err := database.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status == "done" {
				cancel()
				<-done
				return // success
			}
		}
	}
}

func TestDaemonIntegration_WorkerCrash(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("crash test task", "this will fail", "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Mock spawner that exits with error.
	spawner := &daemon.MockSpawner{ExitCode: 1, OutputText: "something broke"}

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   4,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	deadline := time.After(4 * time.Second)
	for {
		select {
		case <-deadline:
			cancel()
			t.Fatal("task was not blocked within timeout")
		case <-time.After(100 * time.Millisecond):
			updated, err := database.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status == "blocked" {
				cancel()
				<-done
				// Verify block reason contains output.
				if updated.BlockReason == nil || *updated.BlockReason == "" {
					t.Error("expected block reason with output")
				}
				return // success
			}
		}
	}
}

func TestDaemonIntegration_MaxWorkers(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create 5 tasks.
	for i := 0; i < 5; i++ {
		database.AddTask("max worker task", "", "", "")
	}

	// Spawner that never exits (blocks on Wait).
	spawner := &hangingSpawner{}

	d := daemon.New(database, daemon.Config{
		MaxWorkers:   2,
		PollInterval: 100 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait a bit for spawning.
	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	// Count active tasks — should be exactly 2.
	active, _ := database.ListTasks("active", false)
	if len(active) != 2 {
		t.Errorf("active tasks = %d, want 2", len(active))
	}
}

// doneCallingSpawner simulates a worker that calls dt done on the task and exits cleanly.
type doneCallingSpawner struct {
	db *db.DB
}

func (s *doneCallingSpawner) Spawn(_ context.Context, task db.Task, _ string) (daemon.WorkerHandle, error) {
	// Simulate: worker does work, calls dt done, exits.
	s.db.DoneTask(task.ID)
	return &immediateHandle{}, nil
}

type immediateHandle struct{}

func (h *immediateHandle) PID() int      { return os.Getpid() }
func (h *immediateHandle) Wait() error   { return nil }
func (h *immediateHandle) Output() string { return "" }

// hangingSpawner creates workers that never exit (for testing max_workers).
type hangingSpawner struct{}

func (s *hangingSpawner) Spawn(_ context.Context, task db.Task, _ string) (daemon.WorkerHandle, error) {
	return &hangingHandle{}, nil
}

type hangingHandle struct{}

func (h *hangingHandle) PID() int      { return os.Getpid() }
func (h *hangingHandle) Wait() error   { select {} } // blocks forever
func (h *hangingHandle) Output() string { return "" }
```

- [ ] **Step 2: Run integration tests**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./tests/ -v -run TestDaemonIntegration -timeout 30s`
Expected: PASS — all three integration tests pass.

- [ ] **Step 3: Commit**

```bash
git add tests/daemon_integration_test.go
git commit -m "test: add daemon integration tests for spawn, crash, and max workers"
```

---

## Task 10: Build Verification + Final Cleanup

Verify both binaries build, all tests pass, clean up any issues.

**Files:**
- No new files.

- [ ] **Step 1: Build both binaries**

Run: `cd /home/tal/Documents/dispatch-ai && go build ./cmd/dt/ && go build ./cmd/dispatchd/`
Expected: Both build without errors.

- [ ] **Step 2: Run all tests**

Run: `cd /home/tal/Documents/dispatch-ai && go test ./... -timeout 60s`
Expected: All tests pass.

- [ ] **Step 3: Run vet and check for issues**

Run: `cd /home/tal/Documents/dispatch-ai && go vet ./...`
Expected: No issues.

- [ ] **Step 4: Clean up build artifacts**

Run: `rm -f dt dispatchd`

- [ ] **Step 5: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "chore: build verification and cleanup"
```
