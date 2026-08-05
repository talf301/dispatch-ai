package daemon

import (
	"bytes"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
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

func TestOrphanBranchFindings(t *testing.T) {
	repo := initTestRepo(t)
	for _, branch := range []string{"dispatch/known1", "dispatch/stray1"} {
		cmd := exec.Command("git", "branch", branch)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create branch %s: %v\n%s", branch, err, out)
		}
	}

	d := &Daemon{repos: map[string]config.RepoConfig{repo: {Path: repo}}}
	findings := d.orphanBranchFindings([]db.Task{{ID: "known1", Status: "active"}})
	if len(findings) != 1 || findings[0].Subject != filepath.Join(repo, "dispatch/stray1") {
		t.Fatalf("findings = %#v, want stray1 only", findings)
	}
}

func TestScanReviewLogsFindingOnce(t *testing.T) {
	repo := initTestRepo(t)
	cmd := exec.Command("git", "branch", "dispatch/stray1")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	d := &Daemon{
		db:    openTestDB(t),
		repos: map[string]config.RepoConfig{repo: {Path: repo}},
	}
	var logs bytes.Buffer
	d.logger = log.New(&logs, "", 0)
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	d.scanReview(now)
	first := logs.String()
	if first == "" {
		t.Fatal("first scan did not log the finding")
	}
	d.scanReview(now.Add(time.Hour))
	if got := logs.String(); got != first {
		t.Errorf("second unchanged scan logged again: %q", got[len(first):])
	}
}
