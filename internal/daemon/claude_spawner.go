package daemon

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// ClaudeSpawner spawns Claude Code CLI processes as workers.
type ClaudeSpawner struct {
	ClaudeBin    string // path to claude binary, default "claude"
	SystemPrompt string // contents of worker.md
	OutputLines  int    // ring buffer size, default 100
}

// Compile-time check that ClaudeSpawner implements WorkerSpawner.
var _ WorkerSpawner = (*ClaudeSpawner)(nil)

func (s *ClaudeSpawner) Spawn(ctx context.Context, task db.Task, workDir string) (WorkerHandle, error) {
	bin := s.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	lines := s.OutputLines
	if lines == 0 {
		lines = 100
	}

	prompt := fmt.Sprintf("Your task ID is %s. Run `dt show %s` to read your assignment.", task.ID, task.ID)

	args := []string{
		"--print",
		"--system-prompt", s.SystemPrompt,
		"--prompt", prompt,
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workDir

	buf := NewRingBuf(lines)
	cmd.Stdout = buf
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start claude: %w", err)
	}

	h := &claudeHandle{cmd: cmd, buf: buf, done: make(chan struct{})}
	go func() {
		h.exitErr = cmd.Wait()
		close(h.done)
	}()

	return h, nil
}

type claudeHandle struct {
	cmd     *exec.Cmd
	buf     *RingBuf
	done    chan struct{}
	exitErr error
}

// Compile-time check that claudeHandle implements WorkerHandle.
var _ WorkerHandle = (*claudeHandle)(nil)

func (h *claudeHandle) PID() int             { return h.cmd.Process.Pid }
func (h *claudeHandle) Done() <-chan struct{} { return h.done }
func (h *claudeHandle) Err() error           { <-h.done; return h.exitErr }
func (h *claudeHandle) Wait() error          { return h.Err() }
func (h *claudeHandle) Output() string       { return h.buf.String() }
