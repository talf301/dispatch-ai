package daemon

import (
	"context"
	"fmt"
	"os"

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

func (m *MockSpawner) Spawn(_ context.Context, task db.Task, _ string) (WorkerHandle, error) {
	if m.SpawnErr != nil {
		return nil, m.SpawnErr
	}
	m.Spawned = append(m.Spawned, task)
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
