// Package agentctx carries the dispatch context injected into every captured
// agent session, either inline via --append-system-prompt or, for the
// capture path, written to disk and passed via --append-system-prompt-file.
// This is how a session knows it's inside a dispatch task without relying on
// anything in the user's global agent config — the contract ships with
// dispatch itself.
package agentctx

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
)

//go:embed session.md
var sessionPrompt string

// SessionPrompt renders the context for one task.
func SessionPrompt(taskID string) string {
	return strings.ReplaceAll(sessionPrompt, "$TASK_ID", taskID)
}

func WriteSessionPrompt(path, taskID string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(SessionPrompt(taskID)), 0o600)
}

// ClaudeArgs builds the claude invocation for a resumed session: the
// dispatch context plus --continue.
func ClaudeArgs(taskID string, rest ...string) []string {
	return append([]string{"claude", "--append-system-prompt", SessionPrompt(taskID)}, rest...)
}
