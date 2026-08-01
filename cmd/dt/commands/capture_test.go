package commands

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadThoughtFileCapturesFileContent(t *testing.T) {
	d := openTestDB(t)
	path := filepath.Join(t.TempDir(), "thought.txt")
	want := "assemble the linked planning context\nwith multiple lines"
	if err := os.WriteFile(path, []byte(want), 0600); err != nil {
		t.Fatal(err)
	}

	thought, err := loadThought(nil, path)
	if err != nil {
		t.Fatalf("loadThought: %v", err)
	}
	task, err := d.CaptureTask(thought, "/repo", "worktree")
	if err != nil {
		t.Fatalf("CaptureTask: %v", err)
	}
	if task.Thought != want {
		t.Errorf("thought = %q, want %q", task.Thought, want)
	}
}
