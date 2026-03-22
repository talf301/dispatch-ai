package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestClaudeSpawner_BuildsCommand(t *testing.T) {
	tmpDir := t.TempDir()
	fakeClaude := filepath.Join(tmpDir, "claude")
	err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho \"working on task\"\nexit 0\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &ClaudeSpawner{
		ClaudeBin:    fakeClaude,
		WorkerPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc1", Title: "Test task", Description: "Do the thing"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker)
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Wait(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if out := handle.Output(); out == "" {
		t.Error("expected some output from fake claude")
	}
}

func TestClaudeSpawner_NonZeroExit(t *testing.T) {
	tmpDir := t.TempDir()
	fakeClaude := filepath.Join(tmpDir, "claude")
	err := os.WriteFile(fakeClaude, []byte("#!/bin/sh\necho \"error output\" >&2\nexit 1\n"), 0o755)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &ClaudeSpawner{
		ClaudeBin:    fakeClaude,
		WorkerPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc2", Title: "Failing task"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker)
	if err != nil {
		t.Fatal(err)
	}

	if err := handle.Wait(); err == nil {
		t.Error("expected error for non-zero exit")
	}

	if out := handle.Output(); out == "" {
		t.Error("expected captured error output")
	}
}
