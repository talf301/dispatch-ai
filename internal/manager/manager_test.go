package manager

import (
	"context"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

type fakeMux struct {
	prompts          int
	ensureWorkspaces int
	createTabs       int
	splitPanes       int
	states           map[string]string
	notified         chan struct{}
}

func (f *fakeMux) EnsureWorkspace(string, string) (string, error) {
	f.ensureWorkspaces++
	return "ws", nil
}
func (f *fakeMux) CreateTab(string, string, string) (string, string, error) {
	f.createTabs++
	return "tab", "pane", nil
}
func (f *fakeMux) RunPane(string, []string) error { return nil }
func (f *fakeMux) SplitPane(string, string) (string, error) {
	f.splitPanes++
	return "tui-pane", nil
}
func (f *fakeMux) PaneExists(string) (bool, error) { return true, nil }
func (f *fakeMux) StartAgent(string, string, string, time.Duration, []string) error {
	return nil
}
func (f *fakeMux) FocusTab(string) error          { return nil }
func (f *fakeMux) RenameTab(string, string) error { return nil }
func (f *fakeMux) AgentStates() (map[string]string, error) {
	if f.states != nil {
		return f.states, nil
	}
	return map[string]string{"pane": "idle"}, nil
}
func (f *fakeMux) AgentStatus(string) (string, error)                         { return "idle", nil }
func (f *fakeMux) CurrentPane() (string, string, string, string, error)       { return "", "", "", "", nil }
func (f *fakeMux) WaitAgent(string, time.Duration, ...string) (string, error) { return "idle", nil }
func (f *fakeMux) PromptAgent(string, string) error {
	f.prompts++
	if f.notified != nil {
		select {
		case f.notified <- struct{}{}:
		default:
		}
	}
	return nil
}
func (f *fakeMux) CloseTab(string) error { return nil }

func TestActionable(t *testing.T) {
	for _, status := range []string{"blocked", "done", "killed", "proposed"} {
		if !actionable(status) {
			t.Fatalf("%s should wake manager", status)
		}
	}
	if actionable("live") || actionable("unattended") {
		t.Fatal("ordinary progress must not wake manager")
	}
}

func TestRunEndsWhenCancelled(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/dispatch.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	m := New(d, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Run(ctx); err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestRunWakesForTransitionFromAnotherDB(t *testing.T) {
	path := t.TempDir() + "/dispatch.db"
	managerDB, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer managerDB.Close()
	writerDB, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writerDB.Close()
	if err := managerDB.SetMeta(paneKey, "pane"); err != nil {
		t.Fatal(err)
	}
	task, err := writerDB.AddTaskWithStatus("work", "", "", "", nil, "active")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeMux{notified: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(managerDB, f).Run(ctx) }()
	if _, err := writerDB.BlockTask(task.ID, "needs a decision"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.notified:
	case <-time.After(2 * time.Second):
		t.Fatal("manager did not wake for cross-DB transition")
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestRunWakesForMultipleTransitionsFromAnotherDB(t *testing.T) {
	path := t.TempDir() + "/dispatch.db"
	managerDB, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer managerDB.Close()
	writerDB, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writerDB.Close()
	if err := managerDB.SetMeta(paneKey, "pane"); err != nil {
		t.Fatal(err)
	}
	first, err := writerDB.AddTaskWithStatus("first", "", "", "", nil, "active")
	if err != nil {
		t.Fatal(err)
	}
	second, err := writerDB.AddTaskWithStatus("second", "", "", "", nil, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writerDB.BlockTask(first.ID, "needs a decision"); err != nil {
		t.Fatal(err)
	}
	if _, err := writerDB.BlockTask(second.ID, "needs a decision"); err != nil {
		t.Fatal(err)
	}
	f := &fakeMux{notified: make(chan struct{}, 2)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- New(managerDB, f).Run(ctx) }()
	for range 2 {
		select {
		case <-f.notified:
		case <-time.After(2 * time.Second):
			t.Fatal("manager did not wake for both cross-DB transitions")
		}
	}
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("got %v", err)
	}
}

func TestNotifyDeduplicatesAcrossManagerRestart(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/dispatch.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.AddTaskWithStatus("blocked work", "", "", "", nil, "proposed")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMeta(paneKey, "pane"); err != nil {
		t.Fatal(err)
	}
	f := &fakeMux{}
	if err := New(d, f).Notify(task.ID); err != nil {
		t.Fatal(err)
	}
	if err := New(d, f).Notify(task.ID); err != nil {
		t.Fatal(err)
	}
	if f.prompts != 1 {
		t.Fatalf("got %d prompts, want one", f.prompts)
	}
}

func TestStartRecoversAndRecreatesDeadPane(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/dispatch.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	f := &fakeMux{}

	if err := New(d, f).Start("/repo"); err != nil {
		t.Fatal(err)
	}
	if f.createTabs != 1 {
		t.Fatalf("first start created %d tabs, want 1", f.createTabs)
	}
	for key, want := range map[string]string{workspaceKey: "ws", tabKey: "tab", paneKey: "pane", tuiPaneKey: "tui-pane"} {
		got, _, err := d.GetMeta(key)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("meta %s = %q, want %q", key, got, want)
		}
	}

	if err := New(d, f).Start("/repo"); err != nil {
		t.Fatal(err)
	}
	if f.createTabs != 1 {
		t.Fatalf("live pane recovery created %d tabs, want 1", f.createTabs)
	}
	if f.splitPanes != 1 {
		t.Fatalf("live pane recovery created %d splits, want 1", f.splitPanes)
	}

	f.states = map[string]string{}
	if err := New(d, f).Start("/repo"); err != nil {
		t.Fatal(err)
	}
	if f.createTabs != 2 {
		t.Fatalf("dead pane recovery created %d tabs, want 2", f.createTabs)
	}
}
