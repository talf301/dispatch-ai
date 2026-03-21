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

		if _, err := os.Stat(wtDir); os.IsNotExist(err) {
			d.logger.Printf("recovery: task %s has no worktree, blocking", task.ID)
			d.db.BlockTask(task.ID, "unknown worker state after daemon restart")
			continue
		}

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

		if isProcessAlive(pid) {
			d.logger.Printf("recovery: task %s has live worker (pid %d), re-adopting", task.ID, pid)
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
			continue
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

// cleanOrphanedWorktrees removes worktree directories that don't correspond
// to any active task.
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
