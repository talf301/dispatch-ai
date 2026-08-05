package daemon

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
	managerpkg "github.com/dispatch-ai/dispatch/internal/manager"
	"github.com/dispatch-ai/dispatch/internal/mux"
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

	// M5: unattended v2 dispatch. Nil Mux disables it (v1 loop unaffected).
	Mux                   mux.Mux
	ReviewerAgent         string // agent CLI for acceptance reviews, default claude
	WorkerModel           string // optional explicit model for workers
	WorkerEscalationModel string // optional model after repeated review rejection
	WorkerEscalateAfter   int    // rejected review rounds before escalation; default 2
	Manager               bool   // run the single human-facing manager session
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
	gpBin                  string                  // path to gp binary, empty if GP integration disabled
	workers                map[string]WorkerHandle // taskID -> handle
	validations            map[string]*validationHandle
	workerRepo             map[string]string    // taskID -> repoPath
	taskRoles              map[string]SpawnRole // taskID -> current role
	noteCountAtReviewStart map[string]int       // taskID -> note count when reviewer was spawned
	retrying               map[string]bool      // taskID -> reviewer repair retry pending
	protocolRetries        map[string]int       // taskID -> malformed reviewer retries
	logger                 *log.Logger

	// M5: unattended v2 watchers (goroutines, unlike the tick-driven v1 path).
	mux                mux.Mux
	reviewerAgent      string
	sessionDir         string
	mu                 sync.Mutex
	watchingUnattended map[string]bool
	manager            *managerpkg.Manager
	managerCwd         string
	runCtx             context.Context
}

// New creates a Daemon from the given config and spawner.
func New(database *db.DB, cfg Config, spawner WorkerSpawner) *Daemon {
	repos := cfg.Repos
	if repos == nil {
		repos = make(map[string]config.RepoConfig)
	}
	d := &Daemon{
		db:                     database,
		cfg:                    cfg,
		spawner:                spawner,
		worktreeBase:           cfg.WorktreeBase,
		repos:                  repos,
		baseBranch:             cfg.BaseBranch,
		workers:                make(map[string]WorkerHandle),
		validations:            make(map[string]*validationHandle),
		workerRepo:             make(map[string]string),
		taskRoles:              make(map[string]SpawnRole),
		noteCountAtReviewStart: make(map[string]int),
		retrying:               make(map[string]bool),
		protocolRetries:        make(map[string]int),
		logger:                 log.New(os.Stderr, "[dispatchd] ", log.LstdFlags),
		mux:                    cfg.Mux,
		reviewerAgent:          cfg.ReviewerAgent,
		sessionDir:             cfg.SessionDir,
		watchingUnattended:     make(map[string]bool),
	}
	if cfg.Manager && cfg.Mux != nil {
		paths := make([]string, 0, len(repos))
		for repo := range repos {
			paths = append(paths, repo)
		}
		if len(paths) == 0 {
			d.logger.Printf("WARNING: --manager enabled but no repository is configured; manager disabled")
		} else {
			sort.Strings(paths)
			d.manager = managerpkg.New(database, cfg.Mux)
			d.managerCwd = paths[0]
		}
	}

	if cfg.GPEnabled {
		gpPath, err := exec.LookPath("gp")
		if err != nil {
			d.logger.Printf("WARNING: --gp enabled but gp binary not found in PATH; disabling GP integration")
		} else {
			d.gpBin = gpPath
			d.logger.Printf("GraphPilot integration enabled (gp at %s)", gpPath)
		}
	}

	return d
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
		validationPID := filepath.Join(wtDir, "validation.pid")
		if _, err := os.Stat(validationPID); err == nil {
			pid, startedAt, readErr := readPIDFile(validationPID)
			_ = os.Remove(validationPID)
			if readErr == nil && isProcessAlive(pid) && startedAt != "" && processStartTime(pid) == startedAt {
				if process, err := os.FindProcess(pid); err == nil {
					_ = process.Signal(syscall.SIGTERM)
				}
			}
			d.logger.Printf("recovery: task %s had an interrupted validation, blocking", task.ID)
			if _, err := d.db.BlockTask(task.ID, "validation interrupted by daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}
		pid, startedAt, err := readPIDFile(pidPath)
		if os.IsNotExist(err) {
			d.logger.Printf("recovery: task %s has no PID file, blocking", task.ID)
			if _, err := d.db.BlockTask(task.ID, "unknown worker state after daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}
		if err != nil {
			d.logger.Printf("recovery: task %s has invalid PID file: %v, blocking", task.ID, err)
			if _, err := d.db.BlockTask(task.ID, "invalid PID file after daemon restart"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}

		// A pid on its own proves nothing: the number may have been recycled
		// while the daemon was down.
		if isProcessAlive(pid) && processStartTime(pid) != startedAt {
			d.logger.Printf("recovery: task %s pid %d belongs to a different process now, blocking", task.ID, pid)
			if _, err := d.db.BlockTask(task.ID, "worker died while daemon was down (PID reused)"); err != nil {
				d.logger.Printf("recovery: block task %s: %v", task.ID, err)
			}
			continue
		}

		// Record repo mapping for recovered tasks. An unresolvable repo is a
		// bookkeeping problem — log it, but don't block a live worker over it.
		repoPath, err := d.taskRepoPath(&task)
		if err != nil {
			d.logger.Printf("recovery: task %s: %v", task.ID, err)
		}

		if isProcessAlive(pid) {
			d.logger.Printf("recovery: task %s has live worker (pid %d), re-adopting", task.ID, pid)
			branchName := fmt.Sprintf("dispatch/%s", task.ID)
			baseBranch, baseErr := d.baseBranchFor(&task)
			committed := func() bool {
				if baseErr != nil {
					return true // can't tell — let the review gate judge the work
				}
				has, err := worktreeBranchHasCommits(wtDir, baseBranch, branchName)
				return err != nil || has
			}
			d.workers[task.ID] = newAdoptedHandle(pid, committed)
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
	process, _ := os.FindProcess(pid) // never fails on Unix
	return process.Signal(syscall.Signal(0)) == nil
}

// processStartTime returns the OS-reported start time of a pid, which is what
// distinguishes the process the daemon spawned from a later reuse of its
// number. Empty when the process is gone or unreadable.
//
// ponytail: shells out to ps (portable across darwin/linux); read /proc or
// pull in a process library only if this ever has to run somewhere without ps.
func processStartTime(pid int) string {
	out, err := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// writePIDFile records a worker's pid together with its start time.
func writePIDFile(path string, pid int) error {
	return os.WriteFile(path, []byte(fmt.Sprintf("%d\n%s\n", pid, processStartTime(pid))), 0o644)
}

// readPIDFile returns the pid and the start time recorded alongside it.
func readPIDFile(path string) (int, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, "", err
	}
	pidLine, startedAt, _ := strings.Cut(strings.TrimSpace(string(b)), "\n")
	pid, err := strconv.Atoi(strings.TrimSpace(pidLine))
	if err != nil {
		return 0, "", fmt.Errorf("parse pid: %w", err)
	}
	return pid, strings.TrimSpace(startedAt), nil
}

// adoptedHandle monitors a process the daemon didn't spawn (re-adopted on restart).
type adoptedHandle struct {
	pid     int
	output  string
	done    chan struct{}
	exitErr error
}

// newAdoptedHandle watches a process the daemon did not spawn, so no exit
// status is available. Clean exit is inferred from the completion marker the
// worker prompt asks for — commits on the task's own branch — since workers are
// explicitly told not to call `dt done`. committed must not touch daemon state.
func newAdoptedHandle(pid int, committed func() bool) *adoptedHandle {
	h := &adoptedHandle{pid: pid, done: make(chan struct{})}
	go func() {
		for isProcessAlive(pid) {
			time.Sleep(1 * time.Second)
		}
		if !committed() {
			h.exitErr = fmt.Errorf("adopted process %d exited without committing to its branch", pid)
		}
		close(h.done)
	}()
	return h
}

func (h *adoptedHandle) PID() int              { return h.pid }
func (h *adoptedHandle) Done() <-chan struct{} { return h.done }
func (h *adoptedHandle) Err() error            { <-h.done; return h.exitErr }
func (h *adoptedHandle) Wait() error           { return h.Err() }
func (h *adoptedHandle) Output() string        { return h.output }

// taskRepoPath returns the repo path for a task. A task without a repo field
// only resolves when exactly one repo is configured — with several, map
// iteration order would pick a different one on every call.
func (d *Daemon) taskRepoPath(task *db.Task) (string, error) {
	if task.Repo != nil {
		if _, ok := d.repos[*task.Repo]; ok {
			return *task.Repo, nil
		}
		// Agents often create child tasks from a dispatch worktree and pass
		// that cwd as -r. Map a worktree back to its registered repository.
		if repo := d.registeredRepoForPath(*task.Repo); repo != "" {
			return repo, nil
		}
		return *task.Repo, nil
	}
	if len(d.repos) == 1 {
		for path := range d.repos {
			return path, nil
		}
	}
	return "", fmt.Errorf("task %s has no repo set and %d repos are configured", task.ID, len(d.repos))
}

func (d *Daemon) registeredRepoForPath(path string) string {
	commonDir := func(p string) string {
		out, err := exec.Command("git", "-C", p, "rev-parse", "--git-common-dir").Output()
		if err != nil {
			return ""
		}
		common := strings.TrimSpace(string(out))
		if !filepath.IsAbs(common) {
			common = filepath.Join(p, common)
		}
		common, err = filepath.Abs(common)
		if err != nil {
			return ""
		}
		if resolved, err := filepath.EvalSymlinks(common); err == nil {
			common = resolved
		}
		return filepath.Clean(common)
	}
	taskCommon := commonDir(path)
	if taskCommon == "" {
		return ""
	}
	for repo := range d.repos {
		if commonDir(repo) == taskCommon {
			return repo
		}
	}
	return ""
}

// baseBranchFor returns the branch a task's worktree branch is based on:
// its plan branch for a child task, otherwise the configured or default branch.
func (d *Daemon) baseBranchFor(task *db.Task) (string, error) {
	if task.ParentID != nil {
		return fmt.Sprintf("dispatch/plan-%s", *task.ParentID), nil
	}
	if task.BaseBranch != nil {
		return *task.BaseBranch, nil
	}
	if d.baseBranch != "" {
		return d.baseBranch, nil
	}
	repoPath, err := d.taskRepoPath(task)
	if err != nil {
		return "", err
	}
	return DetectDefaultBranch(repoPath)
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
	for taskID := range d.workers {
		activePerRepo[d.workerRepo[taskID]]++
	}

	for _, task := range tasks {
		repoPath, err := d.taskRepoPath(&task)
		if err != nil {
			d.logger.Printf("spawn: %v, skipping", err)
			continue
		}

		// Check per-repo capacity.
		repoCfg, ok := d.repos[repoPath]
		if !ok {
			d.logger.Printf("spawn: task %s references unknown repo %q, skipping", task.ID, repoPath)
			continue
		}
		if activePerRepo[repoPath] >= repoCfg.MaxWorkers {
			continue
		}

		baseBranch, err := d.baseBranchFor(&task)
		if err != nil {
			d.logger.Printf("spawn: task %s preflight failed: %v", task.ID, err)
			if _, blockErr := d.db.BlockTask(task.ID, fmt.Sprintf("preflight failed: %v", err)); blockErr != nil {
				d.logger.Printf("spawn: block task %s: %v", task.ID, blockErr)
			}
			continue
		}
		if task.ParentID == nil {
			if _, err := revParse(repoPath, baseBranch); err != nil {
				d.logger.Printf("spawn: task %s preflight failed: %v", task.ID, err)
				if _, blockErr := d.db.BlockTask(task.ID, fmt.Sprintf("preflight failed: base branch %s is unavailable: %v", baseBranch, err)); blockErr != nil {
					d.logger.Printf("spawn: block task %s: %v", task.ID, blockErr)
				}
				continue
			}
		}

		// Claim first to prevent double-spawn.
		sessionID := fmt.Sprintf("dispatchd-%s", task.ID)
		if _, err := d.db.ClaimTask(task.ID, sessionID); err != nil {
			d.logger.Printf("spawn: claim %s: %v (already claimed?)", task.ID, err)
			continue
		}

		wtDir := filepath.Join(d.worktreeBase, task.ID)
		branchName := fmt.Sprintf("dispatch/%s", task.ID)
		branchExisted := BranchExists(repoPath, branchName)
		worktreeExisted := false
		if _, statErr := os.Stat(wtDir); statErr == nil {
			worktreeExisted = true
		}

		// Check if worktree already exists (reopened after review rejection).
		if !worktreeExisted {
			// Worktree doesn't exist — create it.
			// Determine which branch to base the worktree on.
			if task.ParentID != nil {
				parentBranch := fmt.Sprintf("dispatch/plan-%s", *task.ParentID)
				if !BranchExists(repoPath, parentBranch) {
					parent, parentErr := d.db.GetTask(*task.ParentID)
					if parentErr != nil {
						d.logger.Printf("spawn: task %s: %v", task.ID, parentErr)
						d.db.BlockTask(task.ID, fmt.Sprintf("preflight failed: %v", parentErr))
						continue
					}
					base, baseErr := d.baseBranchFor(parent)
					if baseErr != nil {
						d.logger.Printf("spawn: task %s: %v", task.ID, baseErr)
						d.db.BlockTask(task.ID, fmt.Sprintf("preflight failed: %v", baseErr))
						continue
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
				if _, blockErr := d.db.BlockTask(task.ID, fmt.Sprintf("could not create worktree: %v", err)); blockErr != nil {
					d.logger.Printf("spawn: block task %s: %v", task.ID, blockErr)
				}
				continue
			}
		}
		if err := validateWorktree(wtDir, branchName, baseBranch, !branchExisted); err != nil {
			d.logger.Printf("spawn: task %s preflight failed: %v", task.ID, err)
			if !worktreeExisted {
				if removeErr := RemoveWorktree(repoPath, wtDir, branchName, true); removeErr != nil {
					d.logger.Printf("spawn: cleanup failed worktree %s: %v", task.ID, removeErr)
				}
			}
			if _, blockErr := d.db.BlockTask(task.ID, fmt.Sprintf("preflight failed: %v", err)); blockErr != nil {
				d.logger.Printf("spawn: block task %s: %v", task.ID, blockErr)
			}
			continue
		}

		// reject_count is the task's persisted strike count - it survives a
		// daemon restart and a human dt reopen, unlike an in-memory counter.
		round, err := d.db.GetRejectCount(task.ID)
		if err != nil {
			d.logger.Printf("spawn: get reject count %s: %v", task.ID, err)
		}

		// Compute log suffix for session logging.
		logSuffix := ""
		if round > 0 {
			logSuffix = fmt.Sprintf("-%d", round+1)
		}

		// Spawn worker.
		ctx := context.Background()
		model := d.cfg.WorkerModel
		escalateAfter := d.cfg.WorkerEscalateAfter
		if escalateAfter <= 0 {
			escalateAfter = 3
		}
		if d.cfg.WorkerEscalationModel != "" && round >= escalateAfter {
			model = d.cfg.WorkerEscalationModel
			d.logger.Printf("escalating task %s to worker model %s after %d rejected reviews", task.ID, model, round)
		}
		var handle WorkerHandle
		if modelSpawner, ok := d.spawner.(ModelSpawner); ok {
			handle, err = modelSpawner.SpawnWithModel(ctx, task, wtDir, RoleWorker, logSuffix, model)
		} else {
			handle, err = d.spawner.Spawn(ctx, task, wtDir, RoleWorker, logSuffix)
		}
		if err != nil {
			d.logger.Printf("spawn: worker %s: %v", task.ID, err)
			RemoveWorktree(repoPath, wtDir, branchName, true)
			if _, err := d.db.ReleaseTask(task.ID); err != nil {
				d.logger.Printf("spawn: release task %s: %v", task.ID, err)
			}
			continue
		}
		if err := d.db.SetWorkdir(task.ID, wtDir); err != nil {
			d.logger.Printf("spawn: save workdir for %s: %v", task.ID, err)
		}

		// Write PID file.
		pidPath := filepath.Join(wtDir, "worker.pid")
		if err := writePIDFile(pidPath, handle.PID()); err != nil {
			d.logger.Printf("spawn: write PID file %s: %v", task.ID, err)
		}

		d.workers[task.ID] = handle
		d.workerRepo[task.ID] = repoPath
		d.taskRoles[task.ID] = RoleWorker
		delete(d.retrying, task.ID)
		d.logger.Printf("spawned worker for task %s in repo %s (pid %d)", task.ID, repoPath, handle.PID())
		activePerRepo[repoPath]++
	}
}

func (d *Daemon) closeWorkerTab(taskID string) {
	if d.mux == nil {
		return
	}
	task, err := d.db.GetTaskV2(taskID)
	if err != nil || task.HerdrTab == nil || *task.HerdrTab == "" {
		return
	}
	if err := d.mux.CloseTab(*task.HerdrTab); err != nil {
		d.logger.Printf("cleanup: close herdr tab for %s: %v", taskID, err)
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

		// For adopted worker processes, we can't know the real exit code.
		// Check if the worker already called dt done before blocking.
		// Only applies to workers — reviewer exit codes must never be overridden.
		if waitErr != nil && d.taskRoles[taskID] == RoleWorker {
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

		// For workers only: if the worker already called dt done before we
		// could track exit, treat it as a clean exit. Never override reviewer
		// exit codes — a reviewer rejection (non-zero exit) must always be
		// routed to handleReviewerExit regardless of task status.
		waitErr := f.err
		if waitErr != nil && role == RoleWorker {
			if task, err := d.db.GetTask(f.taskID); err == nil && task.Status == "done" {
				waitErr = nil
			}
		}

		if role == RoleReviewer {
			d.handleReviewerResult(f.taskID, handle, waitErr)
		} else if waitErr == nil {
			d.handleWorkerComplete(f.taskID)
		} else {
			d.handleWorkerCrash(f.taskID, waitErr, handle)
		}
	}
}

// handleWorkerComplete spawns a reviewer in the same worktree after a worker exits 0.
func (d *Daemon) handleWorkerComplete(taskID string) {
	wtDir := filepath.Join(d.worktreeBase, taskID)

	// Skip branch validation if the task is already done (worker called dt done).
	task, err := d.db.GetTask(taskID)
	if err != nil {
		d.logger.Printf("review: get task %s: %v", taskID, err)
		return
	}

	// Always verify the worker committed to the worktree branch, not the main repo.
	// This runs regardless of task status — even if the worker called dt done,
	// we must validate the branch before spawning a reviewer.
	branchName := fmt.Sprintf("dispatch/%s", taskID)
	currentBranch, branchErr := worktreeCurrentBranch(wtDir)
	if branchErr != nil {
		d.logger.Printf("review: task %s: %v", taskID, branchErr)
		if _, err := d.db.BlockTask(taskID, fmt.Sprintf("Worker left the task worktree in an invalid git state: %v", branchErr)); err != nil {
			d.logger.Printf("review: block task %s: %v", taskID, err)
		}
		return
	}
	if currentBranch != branchName {
		d.logger.Printf("review: task %s is on branch %s, expected %s", taskID, currentBranch, branchName)
		if _, err := d.db.BlockTask(taskID, fmt.Sprintf("Worker changed the task worktree branch to %s; expected %s", currentBranch, branchName)); err != nil {
			d.logger.Printf("review: block task %s: %v", taskID, err)
		}
		return
	}
	baseBranch, err := d.baseBranchFor(task)
	if err != nil {
		d.logger.Printf("review: task %s: resolve base branch: %v — skipping commit check", taskID, err)
	} else {
		// Only block on a definite "no commits" answer; a failed count is an
		// infrastructure problem, not evidence the worker did nothing.
		has, err := worktreeBranchHasCommits(wtDir, baseBranch, branchName)
		if err != nil {
			d.logger.Printf("review: task %s: %v — skipping commit check", taskID, err)
		} else if !has {
			d.logger.Printf("review: task %s has no commits on branch %s", taskID, branchName)
			if _, err := d.db.BlockTask(taskID, fmt.Sprintf("Worker made no commits on expected branch %s", branchName)); err != nil {
				d.logger.Printf("review: block task %s: %v", taskID, err)
			}
			return
		}
	}

	if repoCfg, ok := d.repos[d.workerRepo[taskID]]; ok && repoCfg.TestCommand != "" {
		if err := d.startValidation(taskID, repoCfg.TestCommand, wtDir); err != nil {
			d.handleValidationFailure(taskID, err.Error(), "")
		}
		return
	}
	d.startReviewer(taskID, task, wtDir)
}

func (d *Daemon) startReviewer(taskID string, task *db.Task, wtDir string) {

	// Record note count before reviewer spawns.
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("review: get notes %s: %v", taskID, err)
		notes = nil
	}
	d.noteCountAtReviewStart[taskID] = len(notes)

	// Compute log suffix for session logging.
	round, err := d.db.GetRejectCount(taskID)
	if err != nil {
		d.logger.Printf("review: get reject count %s: %v", taskID, err)
	}
	logSuffix := fmt.Sprintf("-review-%d", round+1)

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
	if err := writePIDFile(pidPath, handle.PID()); err != nil {
		d.logger.Printf("review: write PID file %s: %v", taskID, err)
	}

	d.workers[taskID] = handle
	d.taskRoles[taskID] = RoleReviewer
	d.logger.Printf("spawned reviewer for task %s (pid %d)", taskID, handle.PID())
}

// validationHandle is a daemon-owned child process. The daemon observes its
// completion without spending a model turn waiting for it.
type validationHandle struct {
	cmd     *exec.Cmd
	done    chan struct{}
	err     error
	output  *RingBuf
	started time.Time
}

func (h *validationHandle) Output() string { return h.output.String() }

func (d *Daemon) startValidation(taskID, command, workdir string) error {
	ctx := d.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	output := NewRingBuf(100)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workdir
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start validation: %w", err)
	}
	h := &validationHandle{cmd: cmd, done: make(chan struct{}), output: output, started: time.Now()}
	if err := writePIDFile(filepath.Join(workdir, "validation.pid"), cmd.Process.Pid); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("record validation: %w", err)
	}
	d.validations[taskID] = h
	go func() {
		h.err = cmd.Wait()
		_ = os.Remove(filepath.Join(workdir, "validation.pid"))
		close(h.done)
	}()
	d.logger.Printf("started validation for task %s (pid %d)", taskID, cmd.Process.Pid)
	return nil
}

func (d *Daemon) monitorValidations() {
	for taskID, h := range d.validations {
		select {
		case <-h.done:
			delete(d.validations, taskID)
			duration := time.Since(h.started).Round(time.Millisecond)
			if h.err == nil {
				d.logger.Printf("validation passed for task %s (duration %s, exit 0)", taskID, duration)
				task, err := d.db.GetTask(taskID)
				if err == nil {
					d.startReviewer(taskID, task, filepath.Join(d.worktreeBase, taskID))
				}
			} else {
				d.handleValidationFailure(taskID, fmt.Sprintf("%v (duration %s, non-zero exit)", h.err, duration), h.Output())
			}
		default:
		}
	}
}

func (d *Daemon) handleValidationFailure(taskID, validationErr, output string) {
	if len(output) > 2000 {
		output = output[len(output)-2000:]
	}
	notes, err := d.db.GetNotes(taskID)
	if err != nil {
		d.logger.Printf("validation: notes %s: %v", taskID, err)
		return
	}
	repairs := 0
	for _, note := range notes {
		if strings.HasPrefix(note.Content, "Validation failure repair") {
			repairs++
		}
	}
	reason := fmt.Sprintf("validation failed: %s\n%s", validationErr, output)
	author := "validation"
	if _, err := d.db.AddNote(taskID, fmt.Sprintf("Validation failure repair %d/1\n%s", repairs+1, reason), &author); err != nil {
		d.logger.Printf("validation: note %s: %v", taskID, err)
	}
	if repairs >= 1 {
		if _, err := d.db.BlockTask(taskID, "validation failed after one repair attempt: "+reason); err != nil {
			d.logger.Printf("validation: block %s: %v", taskID, err)
		}
		return
	}
	if _, err := d.db.ReopenTask(taskID); err != nil {
		d.logger.Printf("validation: reopen %s: %v", taskID, err)
		return
	}
	d.logger.Printf("validation: task %s failed, allowing one repair worker", taskID)
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
			_, acs, err := d.db.DoneTask(taskID)
			if err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			} else {
				d.gpSyncChild(taskID)
			}
			// Every ancestor that completed needs its own PR, not just the
			// immediate parent.
			for i := range acs {
				d.triggerPR(&acs[i])
			}
		}
		if err := RemoveWorktree(repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	} else {
		// A standalone task has no plan branch to accumulate into - it's a
		// plan of one, so it gets the same treatment a finished multi-child
		// plan gets from triggerPR: push its own branch and open its own PR.
		// Land it (push + PR) before marking done or touching the worktree,
		// mirroring the parent path's merge-before-done ordering above, so a
		// crash before this lands leaves the task "active" rather than a
		// silently-done task with no PR - recoverActive already blocks a
		// stuck "active" task for a human to retry, and createPR is safe to
		// retry (a pre-existing PR is treated as success, not an error).
		if _, ok := d.repos[repoPath]; !ok {
			d.logger.Printf("review-done: task %s references unknown repo %q", taskID, repoPath)
			if _, err := d.db.BlockTask(taskID, fmt.Sprintf("repo %q is not a configured dispatch repo", repoPath)); err != nil {
				d.logger.Printf("review-done: block task %s: %v", taskID, err)
			}
			return
		}
		if err := d.createPR(repoPath, branchName, *task); err != nil {
			reason := fmt.Sprintf("pr: %v", err)
			if len(reason) > 4000 {
				reason = reason[:4000]
			}
			d.logger.Printf("review-done: PR creation failed for %s: %v", taskID, err)
			if _, err := d.db.BlockTask(taskID, reason); err != nil {
				d.logger.Printf("review-done: block task %s: %v", taskID, err)
			}
			return
		}
		if task.Status != "done" {
			if _, _, err := d.db.DoneTask(taskID); err != nil {
				d.logger.Printf("review-done: done task %s: %v", taskID, err)
			} else {
				d.gpSyncChild(taskID)
			}
		}
		if err := RemoveWorktree(repoPath, wtDir, branchName, true); err != nil {
			d.logger.Printf("review-done: cleanup worktree %s: %v", taskID, err)
		}
	}
	d.closeWorkerTab(taskID)
	delete(d.protocolRetries, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	delete(d.workerRepo, taskID)
	d.logger.Printf("task %s completed (review approved)", taskID)
}

// gpSyncChild notifies GraphPilot that a dispatch task completed.
// Fire-and-forget: never blocks or fails dispatch operations.
// Safe to call after handleReviewApproval deletes daemon maps — taskID is
// captured by value, and no other daemon state is accessed in the goroutine.
func (d *Daemon) gpSyncChild(taskID string) {
	if d.gpBin == "" {
		return
	}
	go func() {
		cmd := exec.Command(d.gpBin, "sync-child", taskID)
		if out, err := cmd.CombinedOutput(); err != nil {
			d.logger.Printf("gp sync-child %s failed: %v (output: %s)", taskID, err, strings.TrimSpace(string(out)))
		}
	}()
}

// handleReviewerResult requires an explicit approval verdict. Reviewer notes
// get bounded automatic repair attempts; infrastructure failures block.
func (d *Daemon) handleReviewerResult(taskID string, handle WorkerHandle, exitErr error) {
	ok, rejectReason, verdictErr := parseVerdict(handle.Output())

	// Approval is decided by the verdict alone. A reviewer leaving an
	// informational note (e.g. a heads-up for downstream work) must not be
	// mistaken for a rejection.
	if exitErr == nil && verdictErr == nil && ok {
		d.handleReviewApproval(taskID)
		return
	}

	// Explicit content rejection. A compliant reject exits non-zero on
	// purpose (see prompts/reviewer.md), so exitErr can't gate this branch.
	if verdictErr == nil && !ok {
		notes, err := d.db.GetNotes(taskID)
		if err != nil {
			d.logger.Printf("review-result: get notes %s: %v", taskID, err)
		}
		startCount := d.noteCountAtReviewStart[taskID]
		if startCount > len(notes) {
			startCount = len(notes)
		}
		reason := "reviewer rejected the work"
		hadNote := false
		for _, note := range notes[startCount:] {
			if note.Author != nil && *note.Author == "reviewer" {
				reason += "\n\n" + note.Content
				hadNote = true
			}
		}
		if !hadNote {
			reason += "\n\n" + rejectReason
		}
		if len(reason) > 4000 {
			reason = reason[:4000]
		}

		rejections, err := d.db.IncrementRejectCount(taskID)
		if err != nil {
			d.logger.Printf("review-reject: increment reject count %s: %v", taskID, err)
			return
		}
		if rejections < rejectCap {
			d.retrying[taskID] = true
			if _, err := d.db.ReopenTask(taskID); err != nil {
				d.logger.Printf("review-reject: reopen task %s: %v", taskID, err)
				delete(d.retrying, taskID)
				return
			}
			d.logger.Printf("task %s review rejected (repair round %d), reopening", taskID, rejections)
			return
		}
		if _, err := d.db.BlockTask(taskID, reason); err != nil {
			d.logger.Printf("review-reject: block task %s: %v", taskID, err)
			return
		}
		d.closeWorkerTab(taskID)
		delete(d.protocolRetries, taskID)
		delete(d.noteCountAtReviewStart, taskID)
		delete(d.workerRepo, taskID)
		delete(d.retrying, taskID)
		d.logger.Printf("task %s review rejected %d times; blocked", taskID, rejections)
		return
	}

	// Malformed verdict or crash - an infrastructure failure, not a content
	// rejection. Bounded protocol retries, then block outright.
	if exitErr == nil && verdictErr != nil && d.protocolRetries[taskID] < 2 {
		d.protocolRetries[taskID]++
		if err := d.retryReviewer(taskID); err != nil {
			d.logger.Printf("review-result: retry malformed reviewer %s: %v", taskID, err)
		} else {
			d.logger.Printf("task %s reviewer protocol retry %d/2", taskID, d.protocolRetries[taskID])
			return
		}
	}

	reason := "Reviewer did not provide an explicit approval verdict"
	if exitErr != nil {
		reason = fmt.Sprintf("Reviewer exited: %v", exitErr)
	}
	if output := handle.Output(); output != "" {
		reason += "\n\nLast output:\n" + output
	}
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
	repoPath := d.workerRepo[taskID]
	wtDir := filepath.Join(d.worktreeBase, taskID)
	branchName := fmt.Sprintf("dispatch/%s", taskID)
	if _, err := d.db.BlockTask(taskID, reason); err != nil {
		d.logger.Printf("review-result: block task %s: %v", taskID, err)
	}
	if err := RemoveWorktree(repoPath, wtDir, branchName, false); err != nil {
		d.logger.Printf("review-result: cleanup worktree %s: %v", taskID, err)
	}
	d.closeWorkerTab(taskID)
	delete(d.protocolRetries, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	delete(d.workerRepo, taskID)
	delete(d.retrying, taskID)
	d.logger.Printf("task %s blocked: reviewer result was not an approval", taskID)
}

func (d *Daemon) retryReviewer(taskID string) error {
	task, err := d.db.GetTask(taskID)
	if err != nil {
		return err
	}
	wtDir := filepath.Join(d.worktreeBase, taskID)
	logSuffix := fmt.Sprintf("-review-protocol-%d", d.protocolRetries[taskID])
	handle, err := d.spawner.Spawn(context.Background(), *task, wtDir, RoleReviewer, logSuffix)
	if err != nil {
		return err
	}
	if err := writePIDFile(filepath.Join(wtDir, "worker.pid"), handle.PID()); err != nil {
		d.logger.Printf("review: write PID file %s: %v", taskID, err)
	}
	notes, _ := d.db.GetNotes(taskID)
	d.noteCountAtReviewStart[taskID] = len(notes)
	d.workers[taskID] = handle
	d.taskRoles[taskID] = RoleReviewer
	return nil
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
	d.closeWorkerTab(taskID)
	delete(d.protocolRetries, taskID)
	delete(d.noteCountAtReviewStart, taskID)
	delete(d.workerRepo, taskID)
	delete(d.retrying, taskID)
	d.logger.Printf("task %s blocked: %v", taskID, exitErr)
}

// Run starts the daemon main loop. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	d.logger.Println("starting daemon")
	d.runCtx = ctx

	// Ensure sessions directory exists.
	sessDir := filepath.Join(filepath.Dir(d.worktreeBase), "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return fmt.Errorf("create sessions dir: %w", err)
	}

	d.recoverActive()
	d.cleanOrphanedWorktrees()
	if d.manager != nil {
		if err := d.manager.Start(d.managerCwd); err != nil {
			return fmt.Errorf("start manager: %w", err)
		}
		go func() {
			if err := d.manager.Run(ctx); err != nil && ctx.Err() == nil {
				d.logger.Printf("manager: %v", err)
			}
		}()
	}

	ticker := time.NewTicker(d.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			d.logger.Println("shutting down...")
			d.shutdown()
			return ctx.Err()
		case <-ticker.C:
			if err := d.db.SetMeta("daemon_heartbeat", time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
				d.logger.Printf("heartbeat: %v", err)
			}
			d.spawnReady()
			d.monitorWorkers()
			d.monitorValidations()
			d.scanUnattended(ctx)
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
	for taskID := range d.retrying {
		keepIDs[taskID] = true
	}
	// Keep worktrees for tasks reopened by a validation or review failure.
	for taskID := range d.workerRepo {
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
				// Look up from DB if not tracked. Guessing a repo here would
				// run `git worktree remove` against an unrelated repository.
				task, err := d.db.GetTask(entry.Name())
				if err != nil {
					d.logger.Printf("cleanup: worktree %s: %v, leaving in place", entry.Name(), err)
					continue
				}
				repoPath, err = d.taskRepoPath(task)
				if err != nil {
					d.logger.Printf("cleanup: worktree %s: %v, leaving in place", entry.Name(), err)
					continue
				}
			}
			d.logger.Printf("cleanup: removing orphaned worktree %s", entry.Name())
			RemoveWorktree(repoPath, wtDir, branchName, true)
		}
	}
}
