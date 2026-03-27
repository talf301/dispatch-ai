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

// reviewSpawner: worker exits 0, reviewer exits 0 (approves).
type reviewSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newReviewSpawner(database *db.DB) *reviewSpawner {
	return &reviewSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *reviewSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
	}
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

	task, err := database.AddTask("review test", "test review gate", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := newReviewSpawner(database)

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "task done after review", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	cancel()
	<-doneCh

	if count := spawner.spawnCount[task.ID]; count != 2 {
		t.Errorf("spawn count = %d, want 2 (worker + reviewer)", count)
	}
}

// rejectingReviewSpawner: worker exits 0, reviewer rejects once (adds note + exits non-zero),
// then worker exits 0 again, reviewer approves.
type rejectingReviewSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newRejectingReviewSpawner(database *db.DB) *rejectingReviewSpawner {
	return &rejectingReviewSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *rejectingReviewSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	count := s.spawnCount[task.ID]
	done := make(chan struct{})

	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
	}

	if role == daemon.RoleReviewer && count == 2 {
		// First review — reject with feedback note.
		author := "reviewer"
		s.db.AddNote(task.ID, "Review round 1 — REJECTED\n\nIssues:\n- Missing tests", &author)
		close(done)
		return &failHandle{done: done}, nil
	}

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

	task, err := database.AddTask("rejection test", "test review rejection", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := newRejectingReviewSpawner(database)

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	waitForCondition(t, 8*time.Second, 100*time.Millisecond, "task done after retry", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	cancel()
	<-doneCh

	if count := spawner.spawnCount[task.ID]; count != 4 {
		t.Errorf("spawn count = %d, want 4", count)
	}

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

// dtDoneRejectSpawner: worker calls dt done then exits 0,
// reviewer rejects (adds note + exits non-zero).
// Tests that a reviewer rejection overrides a premature dt done.
type dtDoneRejectSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newDtDoneRejectSpawner(database *db.DB) *dtDoneRejectSpawner {
	return &dtDoneRejectSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *dtDoneRejectSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	count := s.spawnCount[task.ID]
	done := make(chan struct{})

	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
		// Simulate worker calling dt done before exiting.
		s.db.DoneTask(task.ID)
		close(done)
		return &immediateHandle{done: done}, nil
	}

	// Reviewer.
	if count == 2 {
		// First review — reject.
		author := "reviewer"
		s.db.AddNote(task.ID, "REJECTED — worker called dt done prematurely", &author)
		close(done)
		return &failHandle{done: done}, nil
	}

	// Second review — approve.
	close(done)
	return &immediateHandle{done: done}, nil
}

func TestDaemonIntegration_ReviewerRejectsAfterWorkerCalledDtDone(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("dt-done bypass test", "test reviewer rejection after dt done", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := newDtDoneRejectSpawner(database)

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	// After rejection, the task should be reopened and a second worker+reviewer cycle
	// should run. Wait for all 4 spawns: worker1, reviewer1 (reject), worker2, reviewer2 (approve).
	waitForCondition(t, 8*time.Second, 100*time.Millisecond, "4 spawns completed", func() bool {
		return spawner.spawnCount[task.ID] >= 4
	})

	cancel()
	<-doneCh

	if count := spawner.spawnCount[task.ID]; count != 4 {
		t.Errorf("spawn count = %d, want 4 (worker + reject-reviewer + worker + approve-reviewer)", count)
	}
}

// crashingReviewerSpawner: worker exits 0, reviewer crashes (exits non-zero, no notes).
type crashingReviewerSpawner struct {
	spawnCount map[string]int
}

func newCrashingReviewerSpawner() *crashingReviewerSpawner {
	return &crashingReviewerSpawner{spawnCount: make(map[string]int)}
}

func (s *crashingReviewerSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	done := make(chan struct{})

	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
	}

	if role == daemon.RoleReviewer {
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

	task, err := database.AddTask("reviewer crash test", "test reviewer crash", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := newCrashingReviewerSpawner()

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

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
