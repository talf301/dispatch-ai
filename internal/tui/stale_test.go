package tui

import (
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestIsStale(t *testing.T) {
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	threshold := 4 * 24 * time.Hour
	old := now.Add(-6 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	fresh := now.Add(-1 * 24 * time.Hour).UTC().Format("2006-01-02 15:04:05")

	cases := []struct {
		name   string
		task   db.Task
		agent  string
		commit time.Time
		want   bool
	}{
		{"old, idle, no commits", db.Task{Status: "live", CreatedAt: old}, "idle", time.Time{}, true},
		{"old, agent absent", db.Task{Status: "live", CreatedAt: old}, "", time.Time{}, true},
		{"old but agent working", db.Task{Status: "live", CreatedAt: old}, "working", time.Time{}, false},
		{"old but agent done (needs a call, not stale)", db.Task{Status: "live", CreatedAt: old}, "done", time.Time{}, false},
		{"fresh task", db.Task{Status: "live", CreatedAt: fresh}, "idle", time.Time{}, false},
		{"old task, recent commit", db.Task{Status: "live", CreatedAt: old}, "idle", now.Add(-2 * 24 * time.Hour), false},
		{"old task, commit predating creation (base branch tip)", db.Task{Status: "live", CreatedAt: old}, "idle", now.Add(-30 * 24 * time.Hour), true},
		{"parked never stale", db.Task{Status: "parked", CreatedAt: old}, "", time.Time{}, false},
	}
	for _, c := range cases {
		if got := isStale(c.task, c.agent, c.commit, now, threshold); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
