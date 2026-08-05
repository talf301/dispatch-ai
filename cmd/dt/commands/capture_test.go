package commands

import (
	"os"
	"path/filepath"
	"testing"
)

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
