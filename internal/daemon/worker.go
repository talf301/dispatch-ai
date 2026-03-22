package daemon

import (
	"context"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// WorkerSpawner starts worker processes for tasks.
type WorkerSpawner interface {
	Spawn(ctx context.Context, task db.Task, workDir string) (WorkerHandle, error)
}

// WorkerHandle monitors a running worker process.
type WorkerHandle interface {
	PID() int
	Wait() error
	Done() <-chan struct{}
	Err() error
	Output() string
}
