package tui

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// Staleness is derived at read time — never stored — so it can't drift out of
// sync with reality. A live task is stale when nothing has been committed in
// its workdir since the threshold and its agent is idle or gone.

// staleAfter returns the staleness threshold (DISPATCH_STALE_DAYS, default 4).
func staleAfter() time.Duration {
	days := 4
	if v := os.Getenv("DISPATCH_STALE_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	return time.Duration(days) * 24 * time.Hour
}

// lastCommitTime returns the committer time of HEAD in dir, zero if none.
func lastCommitTime(dir string) time.Time {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%ct").Output()
	if err != nil {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// parseTaskTime parses the SQLite datetime format tasks use (UTC).
func parseTaskTime(s string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		return time.Time{}
	}
	return t
}

// lastActivity is max(task creation, last commit in the workdir made after
// creation). A worktree's HEAD starts at the base branch tip, so commits
// predating the task don't count as activity on it.
func lastActivity(t db.Task, commit time.Time) time.Time {
	act := parseTaskTime(t.CreatedAt)
	if commit.After(act) {
		act = commit
	}
	return act
}

// isStale reports whether a live task has gone quiet: agent idle or absent
// right now, and no activity within the threshold.
func isStale(t db.Task, agent string, commit time.Time, now time.Time, threshold time.Duration) bool {
	if t.Status != "live" {
		return false
	}
	switch agent {
	case "working", "blocked", "done":
		return false
	}
	return now.Sub(lastActivity(t, commit)) > threshold
}

// staleDays renders the idle duration for the stale lane badge.
func staleDays(t db.Task, commit time.Time, now time.Time) string {
	d := int(now.Sub(lastActivity(t, commit)).Hours() / 24)
	return "idle " + strconv.Itoa(d) + "d"
}
