package tui

import (
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestClassifyDaemonTasksAsUnattended(t *testing.T) {
	for _, status := range []string{"open", "active", "unattended"} {
		if got := classify(db.Task{Status: status}, ""); got != laneUnattended {
			t.Errorf("classify(%q) = %v, want unattended", status, got)
		}
	}
}
