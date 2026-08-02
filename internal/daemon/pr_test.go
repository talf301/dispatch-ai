package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestFormatPRBody_WithNotes(t *testing.T) {
	worker := "worker"
	notes := []db.Note{
		{Content: "Status changed: open → active", Author: strPtr("system")},
		{Content: "Implemented auth: added auth.go, updated routes.go", Author: &worker},
		{Content: "Added database migration for users table", Author: &worker},
		{Content: "Status changed: active → done", Author: strPtr("system")},
	}

	body := formatPRBody(notes)

	if !contains(body, "## Summary") {
		t.Error("body should contain summary header")
	}
	if !contains(body, "- Implemented auth") {
		t.Error("body should contain worker note 1")
	}
	if !contains(body, "- Added database migration") {
		t.Error("body should contain worker note 2")
	}
	if contains(body, "Status changed") {
		t.Error("body should not contain system notes")
	}
	if !contains(body, "dispatch") {
		t.Error("body should contain dispatch footer")
	}
}

func TestFormatPRBody_NoNotes(t *testing.T) {
	body := formatPRBody(nil)

	if !contains(body, "## Summary") {
		t.Error("body should contain summary header")
	}
	if !contains(body, "_No worker notes recorded._") {
		t.Error("body should contain no-notes message")
	}
}

func TestFormatPRBody_OnlySystemNotes(t *testing.T) {
	notes := []db.Note{
		{Content: "Status changed: open → done", Author: strPtr("system")},
	}

	body := formatPRBody(notes)

	// System notes are filtered out, so no bullets should appear.
	if contains(body, "- Status changed") {
		t.Error("body should not contain system notes as bullets")
	}
}

func TestCheckPendingPRs_NoParents(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := t.TempDir()

	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
	}, &MockSpawner{})

	// Should not panic or error with no pending parents.
	daemon.checkPendingPRs()
}

func TestTriggerPR_UnknownRepo(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := t.TempDir()

	parent, _ := d.AddTask("parent plan", "meta", "", "", nil)

	daemon := New(d, Config{
		Repos:        make(map[string]config.RepoConfig),
		WorktreeBase: worktreeBase,
	}, &MockSpawner{})

	// Should handle gracefully (log warning, no panic).
	daemon.triggerPR(&db.AutoComplete{ParentID: parent.ID})
}

// TestCreatePR_AlreadyExistsIsSuccess proves createPR treats "a PR already
// exists for this head" as success rather than an error. Without this, a
// retry after a daemon restart (or a periodic pending-PR check) would
// erroneously block an already-successfully-PR'd task on its very next
// attempt, just because gh refuses to open a duplicate.
func TestCreatePR_AlreadyExistsIsSuccess(t *testing.T) {
	repoDir := initTestRepo(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "clone", "--bare", repoDir, bareDir).CombinedOutput(); err != nil {
		t.Fatalf("clone bare origin: %v\n%s", err, out)
	}
	remoteCmd := exec.Command("git", "remote", "add", "origin", bareDir)
	remoteCmd.Dir = repoDir
	if out, err := remoteCmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	branchCmd := exec.Command("git", "branch", "dispatch/abcd")
	branchCmd.Dir = repoDir
	if out, err := branchCmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	binDir := t.TempDir()
	script := "#!/bin/sh\necho 'a pull request for branch \"dispatch/abcd\" into branch \"main\" already exists:' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	d := openTestDB(t)
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: t.TempDir(),
	}, &MockSpawner{})

	task, err := d.AddTask("abcd task", "test", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := daemon.createPR(repoDir, "dispatch/abcd", *task); err != nil {
		t.Errorf("createPR returned an error for an already-existing PR, want nil: %v", err)
	}
}

func strPtr(s string) *string { return &s }

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsHelper(s, substr)
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
