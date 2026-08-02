package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type commandAudit struct {
	At      string   `json:"at"`
	Command []string `json:"command"`
	Input   string   `json:"input,omitempty"`
	OK      bool     `json:"ok"`
	Error   string   `json:"error,omitempty"`
}

// auditCommand appends one JSON object per TUI-issued dt command. Logging is
// best-effort: a broken audit path must never prevent the requested mutation.
func auditCommand(command []string, input string, commandErr error) {
	path := os.Getenv("DISPATCH_TUI_LOG")
	if path == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return
		}
		path = filepath.Join(home, ".dispatch", "tui-commands.jsonl")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()

	record := commandAudit{At: time.Now().UTC().Format(time.RFC3339Nano), Command: command, Input: input, OK: commandErr == nil}
	if commandErr != nil {
		record.Error = commandErr.Error()
	}
	line, err := json.Marshal(record)
	if err == nil {
		_, _ = f.Write(append(line, '\n'))
	}
}
