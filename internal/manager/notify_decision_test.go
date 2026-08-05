package manager

import (
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestNotifyDecisionPromptsManagerPane(t *testing.T) {
	d, err := db.Open(t.TempDir() + "/dispatch.db")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.AddTask("blocked", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetMeta(paneKey, "pane"); err != nil {
		t.Fatal(err)
	}
	f := &fakeMux{}
	if err := New(d, f).NotifyDecision(task.ID, "Options: leave it blocked."); err != nil {
		t.Fatal(err)
	}
	if f.prompts != 1 {
		t.Fatalf("prompts = %d, want 1", f.prompts)
	}
}
