package db

import (
	"path/filepath"
	"testing"
)

func TestSetReviewing(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	task, err := d.CaptureTask("check this", "/repo", "worktree")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetReviewing(task.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTaskV2(task.ID)
	if err != nil || !got.Reviewing {
		t.Fatalf("reviewing = %v, err = %v", got.Reviewing, err)
	}
}
