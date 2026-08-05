package tui

import (
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestAutomationReviewBadge(t *testing.T) {
	m := Model{}
	badge := m.taskBadge(row{task: db.Task{Status: "unattended", Reviewing: true, RejectCount: 1}})
	if !strings.Contains(badge, "under review · 2") {
		t.Errorf("badge = %q", badge)
	}
}

func TestEnterAttachesActiveAutomationOnDemand(t *testing.T) {
	workdir, repo := "/worktree", "/repo"
	m := Model{flat: []row{{task: db.Task{ID: "abcd", Status: "active", Workdir: &workdir, Repo: &repo}}}}
	_, cmd := m.focusCurrent()
	if cmd == nil {
		t.Fatal("Enter on active automated work did not request an on-demand attachment")
	}
}
