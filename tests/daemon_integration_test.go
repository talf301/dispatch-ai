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

	task, err := database.AddTask("integration test task", "do something", "", "")
	if err != nil {
		t.Fatal(err)
	}

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

	task, err := database.AddTask("crash test task", "this will fail", "", "")
	if err != nil {
		t.Fatal(err)
	}

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
		database.AddTask("max worker task", "", "", "")
	}

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

func (s *doneCallingSpawner) Spawn(_ context.Context, task db.Task, _ string) (daemon.WorkerHandle, error) {
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

func (s *hangingSpawner) Spawn(_ context.Context, task db.Task, _ string) (daemon.WorkerHandle, error) {
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
