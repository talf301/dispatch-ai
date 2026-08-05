package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestStaleFindingsUsesLatestActivity(t *testing.T) {
	old := "2026-08-03 00:00:00"
	recent := "2026-08-04 23:00:00"
	tasks := []db.Task{
		{ID: "old1", Status: "active", UpdatedAt: old},
		{ID: "new1", Status: "active", UpdatedAt: old, LastActivity: &recent},
	}
	findings := staleFindings(tasks, time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	if len(findings) != 1 || findings[0].Subject != "old1" {
		t.Fatalf("findings = %#v, want only old1", findings)
	}
}

func TestOrphanWorktreeFindings(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "gone1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "live1"), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{worktreeBase: base}
	findings := d.orphanWorktreeFindings([]db.Task{{ID: "live1", Status: "active"}})
	if len(findings) != 1 || findings[0].Subject != "gone1" {
		t.Fatalf("findings = %#v, want gone1", findings)
	}
}
