package tests

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
)

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

func integrationRepos(repoDir string, maxWorkers int) map[string]config.RepoConfig {
	return map[string]config.RepoConfig{
		repoDir: {Path: repoDir, MaxWorkers: maxWorkers},
	}
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

	task, err := database.AddTask("integration test task", "do something", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &doneCallingSpawner{db: database}

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
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
			t.Fatal("task was not completed within timeout")
		case <-time.After(100 * time.Millisecond):
			updated, err := database.GetTask(task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if updated.Status == "done" {
				cancel()
				<-done
				return
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

	task, err := database.AddTask("crash test task", "this will fail", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &daemon.MockSpawner{ExitCode: 1, OutputText: "something broke"}

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
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
				if updated.BlockReason == nil || *updated.BlockReason == "" {
					t.Error("expected block reason with output")
				}
				return
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

	for i := 0; i < 5; i++ {
		database.AddTask("max worker task", "", "", "", nil)
	}

	spawner := &hangingSpawner{}

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 2),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	time.Sleep(500 * time.Millisecond)
	cancel()
	<-done

	active, _ := database.ListTasks("active", false)
	if len(active) != 2 {
		t.Errorf("active tasks = %d, want 2", len(active))
	}
}

// doneCallingSpawner simulates a worker that calls dt done then exits.
type doneCallingSpawner struct {
	db *db.DB
}

func (s *doneCallingSpawner) Spawn(_ context.Context, task db.Task, _ string, _ daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.db.DoneTask(task.ID)
	done := make(chan struct{})
	close(done) // immediately done
	return &immediateHandle{done: done}, nil
}

type immediateHandle struct {
	done chan struct{}
}

func (h *immediateHandle) PID() int             { return os.Getpid() }
func (h *immediateHandle) Wait() error          { return nil }
func (h *immediateHandle) Done() <-chan struct{} { return h.done }
func (h *immediateHandle) Err() error           { return nil }
func (h *immediateHandle) Output() string       { return "" }

// hangingSpawner creates workers that never exit.
type hangingSpawner struct{}

func (s *hangingSpawner) Spawn(_ context.Context, task db.Task, _ string, _ daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	return &hangingHandle{done: make(chan struct{})}, nil // never closed
}

type hangingHandle struct {
	done chan struct{}
}

// Use a PID that doesn't exist so SIGTERM during shutdown fails silently
// instead of terminating the test process.
func (h *hangingHandle) PID() int             { return 99999999 }
func (h *hangingHandle) Wait() error          { <-h.done; return nil } // blocks forever
func (h *hangingHandle) Done() <-chan struct{} { return h.done }
func (h *hangingHandle) Err() error           { <-h.done; return nil } // blocks forever
func (h *hangingHandle) Output() string       { return "" }

// fileCommittingSpawner simulates a worker that creates a file, commits, and marks done.
type fileCommittingSpawner struct {
	db *db.DB
}

func (s *fileCommittingSpawner) Spawn(_ context.Context, task db.Task, workDir string, _ daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	// Create a file unique to this task.
	filePath := filepath.Join(workDir, task.ID+".txt")
	if err := os.WriteFile(filePath, []byte("work from "+task.ID), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	// Git add + commit (use -c flags to ensure git config is set in the worktree).
	cmd := exec.Command("git", "add", ".")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "work for "+task.ID)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git commit: %w\n%s", err, out)
	}

	// Mark done.
	s.db.DoneTask(task.ID)

	done := make(chan struct{})
	close(done)
	return &immediateHandle{done: done}, nil
}

// conflictingSpawner writes task-specific content to a shared file to create merge conflicts.
// Unlike fileCommittingSpawner, it does NOT call DoneTask — the daemon's monitorWorkers
// handles marking tasks done after merge (or blocked on conflict).
type conflictingSpawner struct {
	content map[string]string // taskID -> content to write
}

func (s *conflictingSpawner) Spawn(_ context.Context, task db.Task, workDir string, _ daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	content, ok := s.content[task.ID]
	if !ok {
		content = "default content from " + task.ID
	}

	// Write to a shared file — this causes conflicts when both children modify it.
	filePath := filepath.Join(workDir, "shared.txt")
	if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	cmd := exec.Command("git", "add", ".")
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add: %w\n%s", err, out)
	}

	cmd = exec.Command("git", "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "work for "+task.ID)
	cmd.Dir = workDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git commit: %w\n%s", err, out)
	}

	done := make(chan struct{})
	close(done)
	return &immediateHandle{done: done}, nil
}

// waitForCondition polls until the condition returns true or the timeout is reached.
func waitForCondition(t *testing.T, timeout time.Duration, poll time.Duration, desc string, cond func() bool) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for: %s", desc)
		case <-time.After(poll):
			if cond() {
				return
			}
		}
	}
}

func TestDaemonIntegration_PlanLifecycle(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create parent task.
	parent, err := database.AddTask("plan parent", "the parent plan", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Create 3 children: child1 and child2 are parallel, child3 depends on child1.
	child1, err := database.AddTask("child 1", "parallel task 1", parent.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	child2, err := database.AddTask("child 2", "parallel task 2", parent.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// child3 depends on child1 (afterID = child1.ID).
	child3, err := database.AddTask("child 3", "depends on child1", parent.ID, child1.ID, nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &fileCommittingSpawner{db: database}

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for parent to auto-complete (all children done).
	waitForCondition(t, 10*time.Second, 200*time.Millisecond, "parent auto-complete", func() bool {
		p, err := database.GetTask(parent.ID)
		if err != nil {
			return false
		}
		return p.Status == "done"
	})

	cancel()
	<-done

	// Verify all children are done.
	for _, childID := range []string{child1.ID, child2.ID, child3.ID} {
		task, err := database.GetTask(childID)
		if err != nil {
			t.Fatalf("get child %s: %v", childID, err)
		}
		if task.Status != "done" {
			t.Errorf("child %s status = %q, want done", childID, task.Status)
		}
	}

	// Verify parent branch exists.
	parentBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
	if !daemon.BranchExists(repoDir, parentBranch) {
		t.Fatalf("parent branch %s does not exist", parentBranch)
	}

	// Verify files from all 3 tasks exist on the parent branch.
	// Check out parent branch in a temp worktree and look for the files.
	checkDir := t.TempDir()
	cmd := exec.Command("git", "worktree", "add", checkDir, parentBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout parent branch: %v\n%s", err, out)
	}
	defer func() {
		rmCmd := exec.Command("git", "worktree", "remove", checkDir, "--force")
		rmCmd.Dir = repoDir
		rmCmd.Run()
	}()

	for _, childID := range []string{child1.ID, child2.ID, child3.ID} {
		filePath := filepath.Join(checkDir, childID+".txt")
		content, err := os.ReadFile(filePath)
		if err != nil {
			t.Errorf("file %s.txt not found on parent branch: %v", childID, err)
			continue
		}
		expected := "work from " + childID
		if string(content) != expected {
			t.Errorf("file %s.txt content = %q, want %q", childID, string(content), expected)
		}
	}
}

func TestDaemonIntegration_MergeConflict(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Create parent + 2 parallel children.
	parent, err := database.AddTask("conflict parent", "parent with conflicting children", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	child1, err := database.AddTask("conflict child 1", "first child", parent.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	child2, err := database.AddTask("conflict child 2", "second child", parent.ID, "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Both children write different content to the same file.
	spawner := &conflictingSpawner{
		content: map[string]string{
			child1.ID: "content from child 1\nthis is unique to child 1\n",
			child2.ID: "content from child 2\nthis is unique to child 2\n",
		},
	}

	// Use max-workers=2 so both children are spawned in the same tick,
	// before either is merged. This ensures their worktrees branch from
	// the same parent commit, creating a real merge conflict.
	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 2),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Wait for at least one child to be blocked (merge conflict).
	waitForCondition(t, 10*time.Second, 200*time.Millisecond, "merge conflict blocked", func() bool {
		c1, _ := database.GetTask(child1.ID)
		c2, _ := database.GetTask(child2.ID)
		if c1 == nil || c2 == nil {
			return false
		}
		return c1.Status == "blocked" || c2.Status == "blocked"
	})

	cancel()
	<-done

	// One child should be done (merged first), the other blocked (conflict).
	c1, err := database.GetTask(child1.ID)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := database.GetTask(child2.ID)
	if err != nil {
		t.Fatal(err)
	}

	var doneChild, blockedChild *db.Task
	if c1.Status == "done" && c2.Status == "blocked" {
		doneChild, blockedChild = c1, c2
	} else if c2.Status == "done" && c1.Status == "blocked" {
		doneChild, blockedChild = c2, c1
	} else {
		t.Fatalf("expected one done + one blocked, got child1=%s child2=%s", c1.Status, c2.Status)
	}

	_ = doneChild // used for the structural check above

	// Verify the blocked child has a merge conflict reason.
	if blockedChild.BlockReason == nil || !strings.Contains(strings.ToLower(*blockedChild.BlockReason), "merge conflict") {
		reason := "<nil>"
		if blockedChild.BlockReason != nil {
			reason = *blockedChild.BlockReason
		}
		t.Errorf("blocked child reason = %q, want it to contain 'merge conflict'", reason)
	}

	// Verify parent is NOT done (blocked child prevents auto-completion).
	p, err := database.GetTask(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status == "done" {
		t.Error("parent should not be done when a child is blocked")
	}
}
