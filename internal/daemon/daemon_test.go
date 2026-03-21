package daemon

import (
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
