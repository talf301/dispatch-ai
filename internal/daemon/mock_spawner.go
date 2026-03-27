package daemon

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// MockSpawner is a test double that simulates worker processes.
// Exported so integration tests in other packages can use it.
type MockSpawner struct {
	ExitCode   int
	OutputText string
	SpawnErr   error
	Spawned    []db.Task
}

func (m *MockSpawner) Spawn(_ context.Context, task db.Task, workDir string, role SpawnRole, _ string) (WorkerHandle, error) {
	if m.SpawnErr != nil {
		return nil, m.SpawnErr
	}
	m.Spawned = append(m.Spawned, task)

	// Workers with clean exit should commit to the worktree branch.
	if m.ExitCode == 0 && role == RoleWorker {
		mockCommitInWorktree(workDir, task.ID)
	}
	h := &mockHandle{
		pid:      os.Getpid(),
		exitCode: m.ExitCode,
		output:   m.OutputText,
		done:     make(chan struct{}),
	}
	if m.ExitCode != 0 {
		h.exitErr = fmt.Errorf("exit code %d", m.ExitCode)
	}
	close(h.done)
	return h, nil
}

type mockHandle struct {
	pid      int
	exitCode int
	exitErr  error
	output   string
	done     chan struct{}
}

func (h *mockHandle) PID() int             { return h.pid }
func (h *mockHandle) Done() <-chan struct{} { return h.done }
func (h *mockHandle) Err() error           { return h.exitErr }
func (h *mockHandle) Output() string       { return h.output }

func (h *mockHandle) Wait() error {
	<-h.done
	return h.exitErr
}

// mockCommitInWorktree creates a dummy file and commits it in the worktree.
func mockCommitInWorktree(wtDir, taskID string) {
	os.WriteFile(filepath.Join(wtDir, "mock-output.txt"), []byte("work from "+taskID), 0o644)
	cmd := exec.Command("git", "add", "mock-output.txt")
	cmd.Dir = wtDir
	cmd.Run()
	cmd = exec.Command("git", "-c", "user.email=test@test.com", "-c", "user.name=Test", "commit", "-m", "mock work for "+taskID)
	cmd.Dir = wtDir
	cmd.Run()
}
