package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartAgentArgs(t *testing.T) {
	t.Run("claude uses prompt file", func(t *testing.T) {
		got := startAgentArgs("c1", "/tmp/c1.md", "claude")
		if len(got) != 2 || got[0] != "--append-system-prompt-file" || got[1] != "/tmp/c1.md" {
			t.Fatalf("startAgentArgs() = %#v", got)
		}
	})

	t.Run("codex uses initial prompt", func(t *testing.T) {
		got := startAgentArgs("c1", "/tmp/c1.md", "codex")
		if len(got) != 2 || got[0] != "--dangerously-bypass-approvals-and-sandbox" {
			t.Fatalf("startAgentArgs() = %#v", got)
		}
		if !strings.Contains(got[1], "Your task ID is c1") {
			t.Fatalf("codex prompt = %q", got[1])
		}
	})
}

func TestCheckDedupStartsWhenJudgeFails(t *testing.T) {
	d := openTestDB(t)
	closed, err := d.CaptureTask("ship the same widget", "/repo", "worktree")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.KillTask(closed.ID, "superseded"); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(t.TempDir(), "llm-fails")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DISPATCH_LLM_BIN", bin)
	if err := checkDedup(d, "ship the same widget again"); err != nil {
		t.Fatalf("checkDedup blocked capture after judge failure: %v", err)
	}
}
