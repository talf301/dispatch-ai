package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// CLISpawner spawns coding-agent CLI processes (claude or codex) as workers
// and reviewers. Both run non-interactively with permissions bypassed — the
// worktree is the isolation boundary and enforcement lives in the daemon,
// not in the agent's own gates.
type CLISpawner struct {
	Agent          string // "claude" (default) or "codex"
	Bin            string // binary path override, default = Agent name
	WorkerPrompt   string // contents of worker.md (with $TASK_ID placeholder)
	ReviewerPrompt string // contents of reviewer.md (with $TASK_ID placeholder)
	OutputLines    int    // ring buffer size, default 100
	SessionDir     string // path to ~/.dispatch/sessions/
}

// Compile-time check that CLISpawner implements WorkerSpawner.
var _ WorkerSpawner = (*CLISpawner)(nil)

// argv builds the non-interactive invocation for the configured agent.
func (s *CLISpawner) argv(systemPrompt, prompt, model string) (string, []string, error) {
	agent := s.Agent
	if agent == "" {
		agent = "claude"
	}
	bin := s.Bin
	if bin == "" {
		bin = agent
	}
	switch agent {
	case "claude":
		args := []string{
			"--print",
			"--dangerously-skip-permissions",
			"--system-prompt", systemPrompt,
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		return bin, append(args, prompt), nil
	case "codex":
		// codex exec has no system-prompt slot; prepend it to the prompt.
		args := []string{
			"exec",
			"--dangerously-bypass-approvals-and-sandbox",
		}
		if model != "" {
			args = append(args, "--model", model)
		}
		return bin, append(args, systemPrompt+"\n\n"+prompt), nil
	default:
		return "", nil, fmt.Errorf("unknown agent %q (want claude or codex)", agent)
	}
}

func (s *CLISpawner) Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix string) (WorkerHandle, error) {
	return s.SpawnWithModel(ctx, task, workDir, role, logSuffix, "")
}

func (s *CLISpawner) SpawnWithModel(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix, model string) (WorkerHandle, error) {
	lines := s.OutputLines
	if lines == 0 {
		lines = 100
	}

	prompt := fmt.Sprintf("Your task ID is %s. Run `dt show %s` to read your assignment.", task.ID, task.ID)

	systemPrompt := s.WorkerPrompt
	if role == RoleReviewer {
		systemPrompt = s.ReviewerPrompt
	}
	// Substitute $TASK_ID in the system prompt.
	systemPrompt = strings.ReplaceAll(systemPrompt, "$TASK_ID", task.ID)

	// Substitute $PARENT_ID in the system prompt.
	parentID := ""
	if task.ParentID != nil {
		parentID = *task.ParentID
	}
	systemPrompt = strings.ReplaceAll(systemPrompt, "$PARENT_ID", parentID)

	bin, args, err := s.argv(systemPrompt, prompt, model)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir

	buf := NewRingBuf(lines)

	var logFile *os.File
	if s.SessionDir != "" {
		logPath := filepath.Join(s.SessionDir, task.ID+logSuffix+".log")
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		logFile = f
	}

	tw := NewTeeWriter(buf, logFile)
	cmd.Stdout = tw
	cmd.Stderr = tw

	if err := cmd.Start(); err != nil {
		if logFile != nil {
			logFile.Close()
		}
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}

	h := &cliHandle{cmd: cmd, tw: tw, logFile: logFile, done: make(chan struct{})}
	go func() {
		h.exitErr = cmd.Wait()
		if h.logFile != nil {
			h.logFile.Close()
		}
		close(h.done)
	}()

	return h, nil
}

type cliHandle struct {
	cmd     *exec.Cmd
	tw      *TeeWriter
	logFile *os.File
	done    chan struct{}
	exitErr error
}

// Compile-time check that cliHandle implements WorkerHandle.
var _ WorkerHandle = (*cliHandle)(nil)

func (h *cliHandle) PID() int              { return h.cmd.Process.Pid }
func (h *cliHandle) Done() <-chan struct{} { return h.done }
func (h *cliHandle) Err() error            { <-h.done; return h.exitErr }
func (h *cliHandle) Wait() error           { return h.Err() }
func (h *cliHandle) Output() string        { return h.tw.String() }
