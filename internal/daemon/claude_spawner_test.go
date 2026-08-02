package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// writeFakeBin writes a script that echoes its argv and exits with code.
func writeFakeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCLISpawner_BuildsCommand(t *testing.T) {
	tmpDir := t.TempDir()
	fakeClaude := writeFakeBin(t, tmpDir, "claude", "echo \"working on task\"\nexit 0\n")

	spawner := &CLISpawner{
		Bin:          fakeClaude,
		WorkerPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc1", Title: "Test task", Description: "Do the thing"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker, "")
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

func TestCLISpawner_NonZeroExit(t *testing.T) {
	tmpDir := t.TempDir()
	fakeClaude := writeFakeBin(t, tmpDir, "claude", "echo \"error output\" >&2\nexit 1\n")

	spawner := &CLISpawner{
		Bin:          fakeClaude,
		WorkerPrompt: "You are a worker.",
		OutputLines:  10,
	}

	task := db.Task{ID: "abc2", Title: "Failing task"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleWorker, "")
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

func TestCLISpawner_CodexArgv(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodex := writeFakeBin(t, tmpDir, "codex", "echo \"$@\"\nexit 0\n")

	spawner := &CLISpawner{
		Agent:          "codex",
		Bin:            fakeCodex,
		ReviewerPrompt: "You are reviewing task $TASK_ID.",
		OutputLines:    10,
	}

	task := db.Task{ID: "ab12", Title: "Review me"}
	handle, err := spawner.Spawn(context.Background(), task, tmpDir, RoleReviewer, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Wait(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := handle.Output()
	for _, want := range []string{
		"exec",
		"--dangerously-bypass-approvals-and-sandbox",
		"You are reviewing task ab12.", // system prompt folded into the prompt
		"Your task ID is ab12.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("codex argv missing %q; got: %s", want, out)
		}
	}
}

func TestCLISpawner_ExplicitModel(t *testing.T) {
	tmpDir := t.TempDir()
	fakeCodex := writeFakeBin(t, tmpDir, "codex", "echo \"$@\"\nexit 0\n")
	spawner := &CLISpawner{Agent: "codex", Bin: fakeCodex, WorkerPrompt: "worker", OutputLines: 10}
	h, err := spawner.SpawnWithModel(context.Background(), db.Task{ID: "ab15"}, tmpDir, RoleWorker, "", "gpt-5.6-terra")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.Output(), "--model gpt-5.6-terra") {
		t.Fatalf("explicit model missing from argv: %s", h.Output())
	}
}

func TestCLISpawner_UnknownAgent(t *testing.T) {
	spawner := &CLISpawner{Agent: "gemini"}
	_, err := spawner.Spawn(context.Background(), db.Task{ID: "ab13"}, t.TempDir(), RoleWorker, "")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestRoleSpawner_RoutesByRole(t *testing.T) {
	tmpDir := t.TempDir()
	worker := &CLISpawner{Bin: writeFakeBin(t, tmpDir, "w", "echo worker-ran\n"), OutputLines: 10}
	reviewer := &CLISpawner{Bin: writeFakeBin(t, tmpDir, "r", "echo reviewer-ran\n"), OutputLines: 10}
	rs := &RoleSpawner{Worker: worker, Reviewer: reviewer}

	task := db.Task{ID: "ab14"}
	for role, want := range map[SpawnRole]string{
		RoleWorker:   "worker-ran",
		RoleReviewer: "reviewer-ran",
	} {
		h, err := rs.Spawn(context.Background(), task, tmpDir, role, "")
		if err != nil {
			t.Fatal(err)
		}
		h.Wait()
		if !strings.Contains(h.Output(), want) {
			t.Errorf("role %s ran wrong spawner: %s", role, h.Output())
		}
	}
}
