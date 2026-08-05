package tui

import (
	"strings"
	"testing"
	"time"

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

type tuiTestMux struct{}

func (tuiTestMux) EnsureWorkspace(string, string) (string, error) { return "", nil }
func (tuiTestMux) CreateTab(string, string, string) (string, string, error) {
	return "", "", nil
}
func (tuiTestMux) RunPane(string, []string) error           { return nil }
func (tuiTestMux) SplitPane(string, string) (string, error) { return "", nil }
func (tuiTestMux) PaneExists(string) (bool, error)          { return true, nil }
func (tuiTestMux) StartAgent(string, string, string, time.Duration, []string) error {
	return nil
}
func (tuiTestMux) FocusTab(string) error                   { return nil }
func (tuiTestMux) RenameTab(string, string) error          { return nil }
func (tuiTestMux) AgentStates() (map[string]string, error) { return nil, nil }
func (tuiTestMux) AgentStatus(string) (string, error)      { return "", nil }
func (tuiTestMux) CurrentPane() (string, string, string, string, error) {
	return "", "", "", "", nil
}
func (tuiTestMux) WaitAgent(string, time.Duration, ...string) (string, error) {
	return "", nil
}
func (tuiTestMux) PromptAgent(string, string) error { return nil }
func (tuiTestMux) CloseTab(string) error            { return nil }

func TestAutomationFocusedRowShowsRecentNotes(t *testing.T) {
	database, err := db.Open(t.TempDir() + "/t.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.AddTaskWithStatus("automation", "", "", "", nil, "unattended")
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"old", "rejection\nreason", "latest"} {
		if _, err := database.AddNote(task.ID, content, nil); err != nil {
			t.Fatal(err)
		}
	}

	m := New(database, tuiTestMux{}, "", nil, "")
	msg := m.refresh()().(boardMsg)
	rows := msg.rows[laneUnattended]
	if len(rows) != 1 || len(rows[0].notes) != 2 {
		t.Fatalf("recent notes = %#v, want latest two", rows)
	}
	if rows[0].notes[0] != "rejection\nreason" || rows[0].notes[1] != "latest" {
		t.Fatalf("recent notes = %#v", rows[0].notes)
	}

	var rendered strings.Builder
	m.writeRow(&rendered, rows[0], true, 80)
	output := rendered.String()
	if !strings.Contains(output, "rejection reason · latest") {
		t.Errorf("focused row omitted or split recent notes: %q", output)
	}
}
