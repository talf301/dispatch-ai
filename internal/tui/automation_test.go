package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestAutomationBadgesShowPhaseAndRound(t *testing.T) {
	for _, tc := range []struct {
		name string
		task db.Task
		want string
	}{
		{"queued", db.Task{Status: "open"}, "queued · round 1"},
		{"running repair", db.Task{Status: "active", RejectCount: 1}, "running · round 2"},
		{"reviewing", db.Task{Status: "active", Reviewing: true, RejectCount: 1}, "reviewing · round 2"},
		{"walk-away running", db.Task{Status: "unattended", RejectCount: 2}, "running · round 3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := row{task: tc.task}
			if tc.name == "walk-away running" {
				r.agent = "working"
			}
			if badge := (Model{}).taskBadge(r); !strings.Contains(badge, tc.want) {
				t.Errorf("badge = %q, want %q", badge, tc.want)
			}
		})
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

	task, err := database.AddTaskWithStatus("automation", "", "", "", nil, "active")
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
