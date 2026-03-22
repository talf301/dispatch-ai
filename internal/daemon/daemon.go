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

		if isProcessAlive(pid) {
			d.logger.Printf("recovery: task %s has live worker (pid %d), re-adopting", task.ID, pid)
			d.workers[task.ID] = newAdoptedHandle(pid)
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

// spawnReady polls for ready tasks and spawns workers up to MaxWorkers.
func (d *Daemon) spawnReady() {
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

		// Determine which branch to base the worktree on.
		baseBranch := d.baseBranch
		if task.ParentID != nil {
			parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
			if !BranchExists(d.repoPath, parentBranch) {
				base := d.baseBranch
				if base == "" {
					base, _ = DetectDefaultBranch(d.repoPath)
				}
				cmd := exec.Command("git", "branch", parentBranch, base)
				cmd.Dir = d.repoPath
				if out, err := cmd.CombinedOutput(); err != nil {
					d.logger.Printf("spawn: create parent branch %s: %v\n%s", parentBranch, err, out)
					d.db.ReleaseTask(task.ID)
					continue
				}
			}
			baseBranch = parentBranch
		}

		// Create worktree.
		wtDir := filepath.Join(d.worktreeBase, task.ID)
		branchName := fmt.Sprintf("dispatch/%s", task.ID)
		if err := CreateWorktree(d.repoPath, wtDir, branchName, baseBranch); err != nil {
			d.logger.Printf("spawn: worktree %s: %v", task.ID, err)
			if _, err := d.db.ReleaseTask(task.ID); err != nil {
				d.logger.Printf("spawn: release task %s: %v", task.ID, err)
			}
			continue
		}

		// Spawn worker.
		ctx := context.Background()
		handle, err := d.spawner.Spawn(ctx, task, wtDir, RoleWorker)
		if err != nil {
			d.logger.Printf("spawn: worker %s: %v", task.ID, err)
			RemoveWorktree(d.repoPath, wtDir, branchName, true)
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
		d.logger.Printf("spawned worker for task %s (pid %d)", task.ID, handle.PID())
		available--
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
		delete(d.workers, f.taskID)

		if f.err == nil {
			task, err := d.db.GetTask(f.taskID)
			if err != nil {
				d.logger.Printf("monitor: get task %s: %v", f.taskID, err)
				continue
			}

			wtDir := filepath.Join(d.worktreeBase, f.taskID)
			branchName := fmt.Sprintf("dispatch/%s", f.taskID)

			if task.ParentID != nil {
				// Child task — merge into parent branch BEFORE marking done.
				parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
				if err := MergeBranch(d.repoPath, branchName, parentBranch); err != nil {
					d.logger.Printf("monitor: merge %s into %s failed: %v", branchName, parentBranch, err)
					reason := fmt.Sprintf("Merge conflict merging into plan branch:\n%v", err)
					if _, err := d.db.BlockTask(f.taskID, reason); err != nil {
						d.logger.Printf("monitor: block task %s: %v", f.taskID, err)
					}
					// Preserve branch and worktree for human resolution.
					continue
				}
				// Merge succeeded — now mark done (which may auto-complete parent).
				if task.Status != "done" {
					if _, err := d.db.DoneTask(f.taskID); err != nil {
						d.logger.Printf("monitor: done task %s: %v", f.taskID, err)
					}
				}
				// Clean merge — remove child branch and worktree.
				if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
					d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
				}
			} else {
				// Standalone task (no parent) — original behavior.
				if task.Status != "done" {
					if _, err := d.db.DoneTask(f.taskID); err != nil {
						d.logger.Printf("monitor: done task %s: %v", f.taskID, err)
					}
				}
				if err := RemoveWorktree(d.repoPath, wtDir, branchName, true); err != nil {
					d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
				}
			}
			d.logger.Printf("task %s completed", f.taskID)
		} else {
			// Unclean exit. Block with log tail.
			wtDir := filepath.Join(d.worktreeBase, f.taskID)
			branchName := fmt.Sprintf("dispatch/%s", f.taskID)
			output := handle.Output()
			reason := fmt.Sprintf("Worker exited: %v\n\nLast output:\n%s", f.err, output)
			if len(reason) > 4000 {
				reason = reason[:4000]
			}
			if _, err := d.db.BlockTask(f.taskID, reason); err != nil {
				d.logger.Printf("monitor: block task %s: %v", f.taskID, err)
			}
			// Remove worktree but keep branch for inspection.
			if err := RemoveWorktree(d.repoPath, wtDir, branchName, false); err != nil {
				d.logger.Printf("monitor: cleanup worktree %s: %v", f.taskID, err)
			}
			d.logger.Printf("task %s blocked: %v", f.taskID, f.err)
		}
	}
}

// Run starts the daemon main loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Println("starting daemon")

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
	keepIDs := make(map[string]bool, len(activeTasks)+len(blockedTasks))
	for _, t := range activeTasks {
		keepIDs[t.ID] = true
	}
	for _, t := range blockedTasks {
		keepIDs[t.ID] = true
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !keepIDs[entry.Name()] {
			wtDir := filepath.Join(d.worktreeBase, entry.Name())
			branchName := fmt.Sprintf("dispatch/%s", entry.Name())
			d.logger.Printf("cleanup: removing orphaned worktree %s", entry.Name())
			RemoveWorktree(d.repoPath, wtDir, branchName, true)
		}
	}
}
