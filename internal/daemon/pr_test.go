package daemon

import (
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
