package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestClassifyDaemonTasksAsUnattended(t *testing.T) {
	for _, status := range []string{"open", "active", "unattended"} {
		if got := classify(db.Task{Status: status}, ""); got != laneUnattended {
			t.Errorf("classify(%q) = %v, want unattended", status, got)
		}
	}
}

func TestDaemonStatus(t *testing.T) {
	for _, tc := range []struct {
		name string
		seen time.Time
		want string
	}{
		{"missing", time.Time{}, "daemon not running"},
		{"fresh", time.Now().Add(-2 * time.Second), "daemon: last seen"},
		{"stale", time.Now().Add(-16 * time.Second), "daemon not running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (Model{daemonSeen: tc.seen}).daemonStatus()
			if !strings.Contains(got, tc.want) {
				t.Errorf("daemonStatus() = %q, want %q", got, tc.want)
			}
		})
	}
}
