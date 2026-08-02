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

// ModelSpawner is optional so existing test and custom spawners keep working.
// The daemon uses it when a configured escalation model should be explicit.
type ModelSpawner interface {
	SpawnWithModel(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix, model string) (WorkerHandle, error)
}

// RoleSpawner routes each role to its own spawner, so the worker and the
// reviewer can be different agents (e.g. claude writes, codex reviews).
type RoleSpawner struct {
	Worker   WorkerSpawner
	Reviewer WorkerSpawner
}

var _ WorkerSpawner = (*RoleSpawner)(nil)

func (r *RoleSpawner) Spawn(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix string) (WorkerHandle, error) {
	if role == RoleReviewer {
		return r.Reviewer.Spawn(ctx, task, workDir, role, logSuffix)
	}
	return r.Worker.Spawn(ctx, task, workDir, role, logSuffix)
}

func (r *RoleSpawner) SpawnWithModel(ctx context.Context, task db.Task, workDir string, role SpawnRole, logSuffix, model string) (WorkerHandle, error) {
	spawner := r.Worker
	if role == RoleReviewer {
		spawner = r.Reviewer
	}
	if modelSpawner, ok := spawner.(ModelSpawner); ok {
		return modelSpawner.SpawnWithModel(ctx, task, workDir, role, logSuffix, model)
	}
	return spawner.Spawn(ctx, task, workDir, role, logSuffix)
}

// WorkerHandle monitors a running worker process.
type WorkerHandle interface {
	PID() int
	Wait() error
	Done() <-chan struct{}
	Err() error
	Output() string
}
