package daemon

import (
	"context"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestMockSpawner_Success(t *testing.T) {
	spawner := &MockSpawner{ExitCode: 0}
	handle, err := spawner.Spawn(context.Background(), db.Task{ID: "test"}, "/tmp", RoleWorker, "")
	if err != nil {
		t.Fatal(err)
	}
	if handle.PID() <= 0 {
		t.Error("expected positive PID")
	}
	if err := handle.Wait(); err != nil {
		t.Errorf("expected nil error for exit code 0, got: %v", err)
	}
}

func TestMockSpawner_Failure(t *testing.T) {
	spawner := &MockSpawner{ExitCode: 1, OutputText: "something went wrong"}
	handle, err := spawner.Spawn(context.Background(), db.Task{ID: "test"}, "/tmp", RoleWorker, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(); err == nil {
		t.Error("expected error for exit code 1")
	}
	if got := handle.Output(); got != "something went wrong" {
		t.Errorf("output = %q, want %q", got, "something went wrong")
	}
}
