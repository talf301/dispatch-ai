package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	f()
	w.Close()
	os.Stderr = old
	var out bytes.Buffer
	_, _ = io.Copy(&out, r)
	r.Close()
	return out.String()
}

func commandDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return d, path
}

func TestAddAfterWarning(t *testing.T) {
	d, dbPath := commandDB(t)
	blocker, err := d.AddTask("blocker", "", "", "", nil)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	if _, err := d.BlockTask(blocker.ID, "merge conflict"); err != nil {
		d.Close()
		t.Fatal(err)
	}
	d.Close()

	cmd := NewAddCmd()
	cmd.Flags().String("db", dbPath, "")
	cmd.Flags().Bool("json", false, "")
	cmd.SetArgs([]string{"new task", "--after", blocker.ID})
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	warning := captureStderr(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(warning, "currently blocked") || !strings.Contains(warning, "merge conflict") {
		t.Fatalf("expected blocked warning, got %q", warning)
	}

	check, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	tasks, err := check.ListTasks("", true)
	if err != nil {
		t.Fatal(err)
	}
	var childID string
	for _, task := range tasks {
		if task.Title == "new task" {
			childID = task.ID
		}
	}
	if childID == "" {
		t.Fatal("new task was not created")
	}
	blockers, err := check.GetBlockers(childID)
	if err != nil || len(blockers) != 1 || blockers[0].ID != blocker.ID {
		t.Fatalf("dependency was not created: blockers=%v err=%v", blockers, err)
	}
}

func TestAddAfterNonBlockedHasNoWarning(t *testing.T) {
	d, _ := commandDB(t)
	defer d.Close()
	blocker, err := d.AddTask("blocker", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	warning := captureStderr(t, func() { warnIfBlockerIsBlocked(d, blocker.ID) })
	if warning != "" {
		t.Fatalf("unexpected warning: %q", warning)
	}
}

func TestDepBlockedWarningStillCreatesDependency(t *testing.T) {
	d, dbPath := commandDB(t)
	blocker, err := d.AddTask("blocker", "", "", "", nil)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	dependent, err := d.AddTask("dependent", "", "", "", nil)
	if err != nil {
		d.Close()
		t.Fatal(err)
	}
	if _, err := d.BlockTask(blocker.ID, "waiting for approval"); err != nil {
		d.Close()
		t.Fatal(err)
	}
	d.Close()

	cmd := NewDepCmd()
	cmd.Flags().String("db", dbPath, "")
	cmd.Flags().Bool("json", false, "")
	cmd.SetArgs([]string{dependent.ID, blocker.ID})
	warning := captureStderr(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(warning, "currently blocked") {
		t.Fatalf("expected blocked warning, got %q", warning)
	}

	check, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	blockers, err := check.GetBlockers(dependent.ID)
	if err != nil || len(blockers) != 1 || blockers[0].ID != blocker.ID {
		t.Fatalf("dependency was not created: blockers=%v err=%v", blockers, err)
	}
}
