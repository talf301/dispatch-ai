package daemon

import (
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
