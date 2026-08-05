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

	if count := spawner.spawnCount[task.ID]; count != 2 {
		t.Errorf("spawn count = %d, want 2 (worker + reviewer)", count)
	}
}

type silentReviewSpawner struct{}

func (s *silentReviewSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
	}
	done := make(chan struct{})
	close(done)
	if role == daemon.RoleReviewer {
		return &silentHandle{done: done}, nil
	}
	return &immediateHandle{done: done}, nil
}

type silentHandle struct{ done chan struct{} }

func (h *silentHandle) PID() int              { return daemon.FakePID }
func (h *silentHandle) Wait() error           { return nil }
func (h *silentHandle) Done() <-chan struct{} { return h.done }
func (h *silentHandle) Err() error            { return nil }
func (h *silentHandle) Output() string        { return "" }

func TestDaemonIntegration_SilentReviewBlocks(t *testing.T) {
	repoDir := initGitRepo(t)
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	task, err := database.AddTask("silent review", "must not ship", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: filepath.Join(t.TempDir(), "worktrees"),
	}, &silentReviewSpawner{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	go func() { finished <- d.Run(ctx) }()
	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "silent review blocks", func() bool {
		got, err := database.GetTask(task.ID)
		return err == nil && got.Status == "blocked"
	})
	cancel()
	<-finished
}

// rejectingReviewSpawner: worker exits 0, reviewer rejects once, then approves.
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
		return &rejectHandle{done: done}, nil
	}

	close(done)
	return &immediateHandle{done: done}, nil
}

type failHandle struct {
	done chan struct{}
}

func (h *failHandle) PID() int              { return daemon.FakePID }
func (h *failHandle) Wait() error           { <-h.done; return fmt.Errorf("exit code 1") }
func (h *failHandle) Done() <-chan struct{} { return h.done }
func (h *failHandle) Err() error            { return fmt.Errorf("exit code 1") }
func (h *failHandle) Output() string        { return "review failed" }

// rejectHandle simulates a prompt-compliant reviewer rejection: an explicit
// VERDICT: reject line plus a non-zero exit, per prompts/reviewer.md.
type rejectHandle struct {
	done chan struct{}
}

func (h *rejectHandle) PID() int              { return daemon.FakePID }
func (h *rejectHandle) Wait() error           { <-h.done; return fmt.Errorf("exit code 1") }
func (h *rejectHandle) Done() <-chan struct{} { return h.done }
func (h *rejectHandle) Err() error            { return fmt.Errorf("exit code 1") }
func (h *rejectHandle) Output() string        { return "VERDICT: reject - see note" }

func TestDaemonIntegration_ReviewGateRejectionRetries(t *testing.T) {
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

	waitForCondition(t, 8*time.Second, 100*time.Millisecond, "task done after repair retry", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	if count := spawner.spawnCount[task.ID]; count != 4 {
		t.Errorf("spawn count = %d, want 4 (two worker/reviewer cycles)", count)
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

	cancel()
	<-doneCh
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
		return &rejectHandle{done: done}, nil
	}

	// Second review — approve.
	close(done)
	return &immediateHandle{done: done}, nil
}

func TestDaemonIntegration_ReviewerRejectsAfterWorkerCalledDtDoneRetries(t *testing.T) {
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

	// Rejection should trigger one repair cycle even after the worker called dt done.
	waitForCondition(t, 8*time.Second, 100*time.Millisecond, "4 spawns completed", func() bool {
		return spawner.spawnCount[task.ID] >= 4
	})

	cancel()
	<-doneCh

	if count := spawner.spawnCount[task.ID]; count != 4 {
		t.Errorf("spawn count = %d, want 4 (two worker/reviewer cycles)", count)
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

// fakeGh writes a stand-in `gh` binary onto PATH that records its argv (one
// invocation per line, tab-separated) to recordPath and exits 0, so a real
// GitHub account/network is never required to exercise the PR-creation code
// path in tests.
func fakeGh(t *testing.T, recordPath string) {
	t.Helper()
	binDir := t.TempDir()
	script := "#!/bin/sh\necho \"$@\" >> " + recordPath + "\nexit 0\n"
	ghPath := filepath.Join(binDir, "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
}

// addBareOrigin creates a bare clone of repoDir and wires it up as the
// "origin" remote, so `git push origin <branch>` works against a real local
// remote without any network access.
func addBareOrigin(t *testing.T, repoDir string) {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "clone", "--bare", repoDir, bareDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bare origin: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", bareDir)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin remote: %v\n%s", err, out)
	}
}

// TestDaemonIntegration_StandaloneTaskOpensPR proves a completed standalone
// (non-parent) task pushes its branch and opens its own PR - a plan of one -
// instead of merging straight into the base branch (skipping review/CI) or
// vanishing the moment the worktree/branch gets deleted.
func TestDaemonIntegration_StandaloneTaskOpensPR(t *testing.T) {
	repoDir := initGitRepo(t) // already wires up a bare origin + fake gh
	ghRecord := filepath.Join(t.TempDir(), "gh-calls.txt")
	fakeGh(t, ghRecord) // override with our own so we can assert on its calls

	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("standalone pr", "test standalone task opens its own PR", "", "", nil)
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
		return err == nil && updated.Status == "done"
	})

	cancel()
	<-doneCh

	branchName := fmt.Sprintf("dispatch/%s", task.ID)

	// Pushed to origin - the branch content is safe on the remote regardless
	// of what happens to the local worktree/branch afterward.
	lsRemote := exec.Command("git", "ls-remote", "origin", branchName)
	lsRemote.Dir = repoDir
	out, err := lsRemote.Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		t.Errorf("%s not found on origin after approval: %v", branchName, err)
	}

	// gh pr create was invoked with this task's branch as the head.
	ghCalls, err := os.ReadFile(ghRecord)
	if err != nil {
		t.Fatalf("read gh call record: %v", err)
	}
	if !strings.Contains(string(ghCalls), "--head "+branchName) {
		t.Errorf("gh pr create not called with --head %s, got: %s", branchName, ghCalls)
	}

	// Local branch cleaned up post-landing - safe since origin has it.
	cmd := exec.Command("git", "branch", "--list", branchName)
	cmd.Dir = repoDir
	localBranch, _ := cmd.Output()
	if strings.TrimSpace(string(localBranch)) != "" {
		t.Errorf("%s still exists locally after approval, want it deleted post-PR", branchName)
	}
}

// approveWithNoteSpawner: worker exits 0, reviewer approves but also leaves
// an informational note (e.g. a heads-up for downstream work) - a note must
// not be mistaken for a rejection.
type approveWithNoteSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newApproveWithNoteSpawner(database *db.DB) *approveWithNoteSpawner {
	return &approveWithNoteSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *approveWithNoteSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	done := make(chan struct{})
	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
	}
	if role == daemon.RoleReviewer {
		author := "reviewer"
		s.db.AddNote(task.ID, "Round 1 review: APPROVED. Heads-up for downstream work: watch for X.", &author)
	}
	close(done)
	return &immediateHandle{done: done}, nil
}

func TestDaemonIntegration_ApprovalWithNoteIsNotARejection(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("approve with note", "test that an approval note is not a rejection", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	spawner := newApproveWithNoteSpawner(database)

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "task done despite reviewer note", func() bool {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			return false
		}
		return updated.Status == "done"
	})

	if count := spawner.spawnCount[task.ID]; count != 2 {
		t.Errorf("spawn count = %d, want 2 (one worker/reviewer cycle, no repair loop)", count)
	}

	cancel()
	<-doneCh
}

// alwaysRejectSpawner: worker exits 0, reviewer always rejects.
type alwaysRejectSpawner struct {
	db         *db.DB
	spawnCount map[string]int
}

func newAlwaysRejectSpawner(database *db.DB) *alwaysRejectSpawner {
	return &alwaysRejectSpawner{db: database, spawnCount: make(map[string]int)}
}

func (s *alwaysRejectSpawner) Spawn(_ context.Context, task db.Task, workDir string, role daemon.SpawnRole, _ string) (daemon.WorkerHandle, error) {
	s.spawnCount[task.ID]++
	done := make(chan struct{})
	if role == daemon.RoleWorker {
		commitInWorktree(workDir)
		close(done)
		return &immediateHandle{done: done}, nil
	}
	author := "reviewer"
	s.db.AddNote(task.ID, "still not right", &author)
	close(done)
	return &rejectHandle{done: done}, nil
}

// TestDaemonIntegration_RejectCountPersistsAcrossRestart proves the
// reject-count budget is the task's, not the daemon process's: a task that
// already had two rejections in a prior (now-restarted) daemon lifetime must
// block on its very next rejection, not get a fresh three-strike budget.
func TestDaemonIntegration_RejectCountPersistsAcrossRestart(t *testing.T) {
	repoDir := initGitRepo(t)
	dbPath := filepath.Join(t.TempDir(), "test.db")
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTask("persisted rejects", "test reject count survives a daemon restart", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate two rejections from a prior daemon process - the daemon we're
	// about to start has never seen this task before.
	if _, err := database.IncrementRejectCount(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := database.IncrementRejectCount(task.ID); err != nil {
		t.Fatal(err)
	}

	spawner := newAlwaysRejectSpawner(database)

	d := daemon.New(database, daemon.Config{
		Repos:        integrationRepos(repoDir, 4),
		PollInterval: 100 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- d.Run(ctx) }()

	waitForCondition(t, 4*time.Second, 100*time.Millisecond, "task blocked on its third rejection", func() bool {
		updated, err := database.GetTask(task.ID)
		return err == nil && updated.Status == "blocked"
	})

	if count := spawner.spawnCount[task.ID]; count != 2 {
		t.Errorf("spawn count = %d, want 2 (one worker/reviewer cycle - a fresh daemon must not grant 3 more rejections)", count)
	}

	cancel()
	<-doneCh
}
