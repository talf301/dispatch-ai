package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
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

	task, _ := d.AddTask("recover test", "", "", "")
	d.ClaimTask(task.ID, "old-session")

	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, "worker.pid"), []byte("99999999"), 0o644)

	daemon := &Daemon{
		db:           d,
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

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
		logger:       log.New(io.Discard, "", 0),
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
	os.WriteFile(filepath.Join(wtDir, "worker.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)

	daemon := &Daemon{
		db:           d,
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}
}

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

	daemon.spawnReady()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}

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

func TestDaemon_RunAndShutdown(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		MaxWorkers:   4,
		PollInterval: 50 * time.Millisecond,
		RepoPath:     repoDir,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx)
	}()

	time.Sleep(200 * time.Millisecond)

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

	daemon.spawnReady()

	// Manually mark task done (simulating worker calling dt done).
	d.DoneTask(task.ID)

	// Now monitor should detect the exit and clean up.
	daemon.monitorWorkers()

	// Worker should be removed from the map.
	if _, exists := daemon.workers[task.ID]; exists {
		t.Error("worker still in map after clean exit")
	}
}

