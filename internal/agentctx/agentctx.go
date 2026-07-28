// Package agentctx carries the dispatch context injected into every captured
// agent session via --append-system-prompt. This is how a session knows it's
// inside a dispatch task without relying on anything in the user's global
// agent config — the contract ships with dispatch itself.
package agentctx

import (
	_ "embed"
	"os"
	"strings"
)

//go:embed session.md
var sessionPrompt string

// SessionPrompt renders the context for one task.
func SessionPrompt(taskID string) string {
	return strings.ReplaceAll(sessionPrompt, "$TASK_ID", taskID)
}

func WriteSessionPrompt(path, taskID string) error {
	return os.WriteFile(path, []byte(SessionPrompt(taskID)), 0o600)
}

// ClaudeArgs builds the claude invocation for a captured session: the
// dispatch context plus either an opening message or --continue.
func ClaudeArgs(taskID string, rest ...string) []string {
	return append([]string{"claude", "--append-system-prompt", SessionPrompt(taskID)}, rest...)
}
