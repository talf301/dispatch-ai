package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestCreatePR_ZeroDiffSkipsGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 0)
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", ghDir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })

	d := openTestDB(t)
	repo := repoDir
	parent, err := d.AddTask("already merged", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	createPRTestBranch(t, repoDir, parent.ID)
	daemon := New(d, Config{BaseBranch: "main"}, &MockSpawner{})

	if err := daemon.createPR(repoDir, *parent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was invoked for zero-diff branch: %v", err)
	}
	notes, err := d.GetNotes(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !contains(notes[0].Content, "no commits relative to main") {
		t.Fatalf("unexpected zero-diff note: %+v", notes)
	}
}

func TestCreatePR_GitCountErrorFallsThroughToGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 1)
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", ghDir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })

	d := openTestDB(t)
	repo := repoDir
	parent, err := d.AddTask("count error", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	createPRTestBranch(t, repoDir, parent.ID)
	daemon := New(d, Config{BaseBranch: "missing-base"}, &MockSpawner{})

	if err := daemon.createPR(repoDir, *parent); err == nil || !contains(err.Error(), "gh pr create") {
		t.Fatalf("createPR error = %v, want gh failure", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh was not invoked after git count error: %v", err)
	}
}

func initPRTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := initTestRepo(t)
	for _, args := range [][]string{{"git", "branch", "-M", "main"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "--bare", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare repo: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	return repoDir
}

func createPRTestBranch(t *testing.T, repoDir, taskID string) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-b", "dispatch/plan-"+taskID)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create plan branch: %v\n%s", err, out)
	}
}

func writeFakeGH(t *testing.T, dir, marker string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\nprintf invoked > %q\nexit %d\n", marker, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

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
