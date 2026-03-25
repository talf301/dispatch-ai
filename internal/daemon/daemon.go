package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

// Config holds daemon configuration.
type Config struct {
	DBPath       string
	Repos        map[string]config.RepoConfig // repoPath -> RepoConfig
	BaseBranch   string                       // empty = auto-detect
	PollInterval time.Duration
	WorktreeBase string // default ~/.dispatch/worktrees
	SessionDir   string // path to ~/.dispatch/sessions/
	GPEnabled    bool   // Enable GraphPilot integration (gp sync-child on task completion)
}

// DefaultConfig returns configuration with defaults.
func DefaultConfig() Config {
	home, _ := os.UserHomeDir()
	return Config{
		DBPath:       filepath.Join(home, ".dispatch", "dispatch.db"),
		Repos:        make(map[string]config.RepoConfig),
		PollInterval: 5 * time.Second,
		WorktreeBase: filepath.Join(home, ".dispatch", "worktrees"),
	}
}

// Daemon orchestrates worker processes.
type Daemon struct {
	db                     *db.DB
	cfg                    Config
	spawner                WorkerSpawner
	worktreeBase           string
	repos                  map[string]config.RepoConfig // repoPath -> RepoConfig
	baseBranch             string
	workers                map[string]WorkerHandle // taskID -> handle
	workerRepo             map[string]string        // taskID -> repoPath
	taskRoles              map[string]SpawnRole     // taskID -> current role
	reviewRound            map[string]int           // taskID -> review round count
	noteCountAtReviewStart map[string]int           // taskID -> note count when reviewer was spawned
	logger                 *log.Logger
}

// New creates a Daemon from the given config and spawner.
func New(database *db.DB, cfg Config, spawner WorkerSpawner) *Daemon {
	repos := cfg.Repos
	if repos == nil {
		repos = make(map[string]config.RepoConfig)
	}
	return &Daemon{
		db:                     database,
		cfg:                    cfg,
		spawner:                spawner,
		worktreeBase:           cfg.WorktreeBase,
		repos:                  repos,
		baseBranch:             cfg.BaseBranch,
		workers:                make(map[string]WorkerHandle),
		workerRepo:             make(map[string]string),
		taskRoles:              make(map[string]SpawnRole),
		reviewRound:            make(map[string]int),
		noteCountAtReviewStart: make(map[string]int),
		logger:                 log.New(os.Stderr, "[dispatchd] ", log.LstdFlags),
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

		if _, err := os.Stat(wtDir); os.IsNotExist(err) {
			d.logger.Printf("recovery: task %s has no worktree, blocking", task.ID)
			if _, err := d.db.BlockTask(task.ID, "unknown worker state after daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}

		pidPath := filepath.Join(wtDir, "worker.pid")
		pidBytes, err := os.ReadFile(pidPath)
		if err != nil {
			d.logger.Printf("recovery: task %s has no PID file, blocking", task.ID)
			if _, err := d.db.BlockTask(task.ID, "unknown worker state after daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}

		pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if err != nil {
			d.logger.Printf("recovery: task %s has invalid PID file, blocking", task.ID)
			if _, err := d.db.BlockTask(task.ID, "invalid PID file after daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}

		// Record repo mapping for recovered tasks.
		repoPath := d.taskRepoPath(&task)

		if isProcessAlive(pid) {
			d.logger.Printf("recovery: task %s has live worker (pid %d), re-adopting", task.ID, pid)
			d.workers[task.ID] = newAdoptedHandle(pid)
			d.workerRepo[task.ID] = repoPath
		} else {
			d.logger.Printf("recovery: task %s worker (pid %d) is dead, blocking", task.ID, pid)
			if _, err := d.db.BlockTask(task.ID, "worker died while daemon was down"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
		}
	}
}

// isProcessAlive checks if a process with the given PID exists.
func isProcessAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// adoptedHandle monitors a process the daemon didn't spawn (re-adopted on restart).
type adoptedHandle struct {
	pid     int
	output  string
	done    chan struct{}
	exitErr error
}

func newAdoptedHandle(pid int) *adoptedHandle {
	h := &adoptedHandle{pid: pid, done: make(chan struct{})}
	go func() {
		for isProcessAlive(pid) {
			time.Sleep(1 * time.Second)
		}
		h.exitErr = fmt.Errorf("adopted process %d exited (status unknown)", pid)
		close(h.done)
	}()
	return h
}

func (h *adoptedHandle) PID() int             { return h.pid }
func (h *adoptedHandle) Done() <-chan struct{} { return h.done }
func (h *adoptedHandle) Err() error           { <-h.done; return h.exitErr }
func (h *adoptedHandle) Wait() error          { return h.Err() }
func (h *adoptedHandle) Output() string       { return h.output }

// taskRepoPath returns the repo path for a task, falling back to the first
// configured repo if the task has no repo field set.
func (d *Daemon) taskRepoPath(task *db.Task) string {
	if task.Repo != nil {
		return *task.Repo
	}
	// Fallback: use the first (only) repo if there's exactly one.
	for path := range d.repos {
		return path
	}
	return "."
}

// spawnReady polls for ready tasks and spawns workers, enforcing per-repo max_workers.
func (d *Daemon) spawnReady() {
	tasks, err := d.db.ReadyTasks()
	if err != nil {
		d.logger.Printf("poll: ready tasks: %v", err)
		return
	}

	// Count active workers per repo.
	activePerRepo := make(map[string]int)
	for _, repoPath := range d.workerRepo {
		activePerRepo[repoPath]++
	}

	for _, task := range tasks {
		repoPath := d.taskRepoPath(&task)

		// Check per-repo capacity.
		repoCfg, ok := d.repos[repoPath]
		if !ok {
			d.logger.Printf("spawn: task %s references unknown repo %q, skipping", task.ID, repoPath)
			continue
		}
		if activePerRepo[repoPath] >= repoCfg.MaxWorkers {
			continue
		}

		// Claim first to prevent double-spawn.
		sessionID := fmt.Sprintf("dispatchd-%s", task.ID)
		if _, err := d.db.ClaimTask(task.ID, sessionID); err != nil {
			d.logger.Printf("spawn: claim %s: %v (already claimed?)", task.ID, err)
			continue
		}

		wtDir := filepath.Join(d.worktreeBase, task.ID)
		branchName := fmt.Sprintf("dispatch/%s", task.ID)

		// Check if worktree already exists (reopened after review rejection).
		if _, statErr := os.Stat(wtDir); statErr != nil {
			// Worktree doesn't exist — create it.
			// Determine which branch to base the worktree on.
			baseBranch := d.baseBranch
			if task.ParentID != nil {
				parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
				if !BranchExists(repoPath, parentBranch) {
					base := d.baseBranch
					if base == "" {
						base, _ = DetectDefaultBranch(repoPath)
					}
					cmd := exec.Command("git", "branch", parentBranch, base)
					cmd.Dir = repoPath
					if out, err := cmd.CombinedOutput(); err != nil {
						d.logger.Printf("spawn: create parent branch %s: %v\n%s", parentBranch, err, out)
						d.db.ReleaseTask(task.ID)
						continue
					}
				}
				baseBranch = parentBranch
			}

			if err := CreateWorktree(repoPath, wtDir, branchName, baseBranch); err != nil {
				d.logger.Printf("spawn: worktree %s: %v", task.ID, err)
				if _, err := d.db.ReleaseTask(task.ID); err != nil {
					d.logger.Printf("spawn: release task %s: %v", task.ID, err)
				}
				continue
			}
		}

		// Recover review round from existing log files (handles daemon restart).
		if _, ok := d.reviewRound[task.ID]; !ok {
			d.reviewRound[task.ID] = recoverReviewRound(d.cfg.SessionDir, task.ID)
		}

		// Compute log suffix for session logging.
		round := d.reviewRound[task.ID]
		logSuffix := ""
		if round > 0 {
			logSuffix = fmt.Sprintf("-%d", round+1)
		}

		// Spawn worker.
		ctx := context.Background()
		handle, err := d.spawner.Spawn(ctx, task, wtDir, RoleWorker, logSuffix)
		if err != nil {
			d.logger.Printf("spawn: worker %s: %v", task.ID, err)
			RemoveWorktree(repoPath, wtDir, branchName, true)
			if _, err := d.db.ReleaseTask(task.ID); err != nil {
				d.logger.Printf("spawn: release task %s: %v", task.ID, err)
			}
			continue
		}

		// Write PID file.
		pidPath := filepath.Join(wtDir, "worker.pid")
		if err := os.WriteFile(pidPath, []byte(strconv.Itoa(handle.PID())), 0o644); err != nil {
			d.logger.Printf("spawn: write PID file %s: %v", task.ID, err)
		}

		d.workers[task.ID] = handle
		d.workerRepo[task.ID] = repoPath
		d.taskRoles[task.ID] = RoleWorker
		d.logger.Printf("spawned worker for task %s in repo %s (pid %d)", task.ID, repoPath, handle.PID())
		activePerRepo[repoPath]++
	}
}

// monitorWorkers checks each active worker using the Done() channel
// for non-blocking exit detection.
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

		// For adopted processes, we can't know the real exit code.
		// Check if the worker already called dt done before blocking.
		if waitErr != nil {
			if task, err := d.db.GetTask(taskID); err == nil && task.Status == "done" {
				waitErr = nil // Worker completed successfully before we could track it.
			}
		}

		finished = append(finished, struct {
			taskID string
			err    error
		}{taskID, waitErr})
	}

	for _, f := range finished {
		handle := d.workers[f.taskID]
		role := d.taskRoles[f.taskID]
		delete(d.workers, f.taskID)
		delete(d.taskRoles, f.taskID)
		// Note: workerRepo is NOT deleted here — it's needed by handleReviewApproval etc.

		// Preserve adopted-process check.
		waitErr := f.err
		if waitErr != nil {
			if task, err := d.db.GetTask(f.taskID); err == nil && task.Status == "done" {
				waitErr = nil
			}
		}

		if waitErr == nil {
			if role == RoleReviewer {
				d.handleReviewApproval(f.taskID)
			} else {
				d.handleWorkerComplete(f.taskID)
			}
		} else {
			if role == RoleReviewer {
				d.handleReviewerExit(f.taskID, handle)
			} else {
				d.handleWorkerCrash(f.taskID, waitErr, handle)
			}
		}
	}
}

// recoverReviewRound globs session log files to determine the current review round.
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

// handleWorkerComplete spawns a reviewer in the same worktree after a worker exits 0.
func (d *Daemon) handleWorkerComplete(taskID string) {
	wtDir := filepath.Join(d.worktreeBase, taskID)

	task, err := d.db.GetTask(taskID)
	if err != nil {
		d.logger.Printf("review: get task %s: %v", taskID, err)
		return
	}

	// Record note count before reviewer spawns.
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("review: get notes %s: %v", taskID, err)
		notes = nil
	}
	d.noteCountAtReviewStart[taskID] = len(notes)

	// Compute log suffix for session logging.
	round := d.reviewRound[taskID] + 1
	logSuffix := fmt.Sprintf("-review-%d", round)

	ctx := context.Background()
	handle, err := d.spawner.Spawn(ctx, *task, wtDir, RoleReviewer, logSuffix)
	if err != nil {
		d.logger.Printf("review: spawn reviewer %s: %v", taskID, err)
		if _, err := d.db.BlockTask(taskID, fmt.Sprintf("failed to spawn reviewer: %v", err)); err != nil {
			d.logger.Printf("review: block task %s: %v", taskID, err)
		}
		return
	}

	// Write PID file for reviewer.
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

	repoPath := d.workerRepo[taskID]
	wtDir := filepath.Join(d.worktreeBase, taskID)
	branchName := fmt.Sprintf("dispatch/%s", taskID)

	if task.ParentID != nil {
		parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
		if err := MergeBranch(repoPath, branchName, parentBranch); err != nil {
			d.logger.Printf("review-done: merge %s into %s failed: %v", branchName, parentBranch, err)
			reason := fmt.Sprintf("Merge conflict merging into plan branch:\n%v", err)
			if _, err := d.db.BlockTask(taskID, reason); err != nil {
				d.logger.Printf("review-done: block task %s: %v", taskID, err)
			}
			return
		}
		if task.Status != "done" {
			_, ac, err := d.db.DoneTask(taskID)
			if err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			}
			if ac != nil {
				d.triggerPR(ac)
			}
		}
		if err := RemoveWorktree(repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	} else {
		if task.Status != "done" {
			if _, _, err := d.db.DoneTask(taskID); err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			}
		}
		if err := RemoveWorktree(repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	}
	delete(d.reviewRound, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	delete(d.workerRepo, taskID)
	d.logger.Printf("task %s completed (review approved)", taskID)
}

// handleReviewerExit handles a reviewer that exited non-zero.
// Compares current note count to the count recorded when the reviewer was spawned.
func (d *Daemon) handleReviewerExit(taskID string, handle WorkerHandle) {
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("review-exit: get notes %s: %v", taskID, err)
	}

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
		repoPath := d.workerRepo[taskID]
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
		if err := RemoveWorktree(repoPath, wtDir, branchName, false); err != nil {
			d.logger.Printf("review-crash: cleanup worktree %s: %v", taskID, err)
		}
		delete(d.reviewRound, taskID)
		delete(d.noteCountAtReviewStart, taskID)
		delete(d.workerRepo, taskID)
		d.logger.Printf("task %s blocked: reviewer crashed", taskID)
	}
}

// handleWorkerCrash blocks a crashed worker with log context.
func (d *Daemon) handleWorkerCrash(taskID string, exitErr error, handle WorkerHandle) {
	repoPath := d.workerRepo[taskID]
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
	if err := RemoveWorktree(repoPath, wtDir, branchName, false); err != nil {
		d.logger.Printf("monitor: cleanup worktree %s: %v", taskID, err)
	}
	delete(d.reviewRound, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	delete(d.workerRepo, taskID)
	d.logger.Printf("task %s blocked: %v", taskID, exitErr)
}

// Run starts the daemon main loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Println("starting daemon")

	// Ensure sessions directory exists.
	sessDir := filepath.Join(filepath.Dir(d.worktreeBase), "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

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
			d.checkPendingPRs()
			d.cleanOrphanedWorktrees()
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

	deadline := time.After(30 * time.Second)
	for len(d.workers) > 0 {
		select {
		case <-deadline:
			d.logger.Printf("timeout: %d workers still running, exiting anyway", len(d.workers))
			return
		case <-time.After(500 * time.Millisecond):
			// Deleting from a map during range iteration is safe in Go.
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

// cleanOrphanedWorktrees removes worktree directories that don't correspond
// to any active or blocked task. Blocked tasks may have worktrees preserved
// for human merge conflict resolution.
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
	blockedTasks, err := d.db.ListTasks("blocked", false)
	if err != nil {
		d.logger.Printf("cleanup: list blocked tasks: %v", err)
		return
	}
	keepIDs := make(map[string]bool, len(activeTasks)+len(blockedTasks)+len(d.workers))
	for _, t := range activeTasks {
		keepIDs[t.ID] = true
	}
	for _, t := range blockedTasks {
		keepIDs[t.ID] = true
	}
	// Also keep worktrees for tasks the daemon is actively managing
	// (e.g., reviewer running after worker marked task done).
	for taskID := range d.workers {
		keepIDs[taskID] = true
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !keepIDs[entry.Name()] {
			wtDir := filepath.Join(d.worktreeBase, entry.Name())
			branchName := fmt.Sprintf("dispatch/%s", entry.Name())
			// Determine repo for this orphaned worktree.
			repoPath := d.workerRepo[entry.Name()]
			if repoPath == "" {
				// Look up from DB if not tracked.
				if task, err := d.db.GetTask(entry.Name()); err == nil {
					repoPath = d.taskRepoPath(task)
				} else {
					// Fallback to first repo.
					for p := range d.repos {
						repoPath = p
						break
					}
				}
			}
			d.logger.Printf("cleanup: removing orphaned worktree %s", entry.Name())
			RemoveWorktree(repoPath, wtDir, branchName, true)
		}
	}
}
