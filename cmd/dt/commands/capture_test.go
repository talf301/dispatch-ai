package commands

import (
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
