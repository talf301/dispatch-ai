package daemon

import (
	"context"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// SpawnRole indicates whether a spawned process is a worker or reviewer.
type SpawnRole string

const (
	RoleWorker   SpawnRole = "worker"
	RoleReviewer SpawnRole = "reviewer"
)

// WorkerSpawner starts worker processes for tasks.
type WorkerSpawner interface {
	Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix string) (WorkerHandle, error)
}

// WorkerHandle monitors a running worker process.
type WorkerHandle interface {
	PID() int
	Wait() error
	Done() <-chan struct{}
	Err() error
	Output() string
}
