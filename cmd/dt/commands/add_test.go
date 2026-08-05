package commands

import (
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func TestWarnIfOrphanFromLivePlan(t *testing.T) {
	d := openTestDB(t)

	liveRepo := "/repo/live-session"
	live, err := d.AddTaskWithStatus("planning session", "", "", "", &liveRepo, "live")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("warns when repo matches a live task and no parent given", func(t *testing.T) {
		w := warnIfOrphanFromLivePlan(d, &liveRepo, "")
		if w == "" {
			t.Fatal("expected a warning, got none")
		}
		if !strings.Contains(w, live.ID) {
			t.Errorf("warning should mention the live task %s, got: %s", live.ID, w)
		}
	})

	t.Run("no warning with a parent given", func(t *testing.T) {
		if w := warnIfOrphanFromLivePlan(d, &liveRepo, "somepid"); w != "" {
			t.Errorf("expected no warning when a parent is given, got: %s", w)
		}
	})

	t.Run("no warning for an unrelated repo", func(t *testing.T) {
		other := "/repo/unrelated"
		if w := warnIfOrphanFromLivePlan(d, &other, ""); w != "" {
			t.Errorf("expected no warning for an unrelated repo, got: %s", w)
		}
	})

	t.Run("no warning with no repo", func(t *testing.T) {
		if w := warnIfOrphanFromLivePlan(d, nil, ""); w != "" {
			t.Errorf("expected no warning with a nil repo, got: %s", w)
		}
	})
}

func TestAddStoresBaseBranch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "dispatch.db")
	root := &cobra.Command{Use: "dt"}
	root.PersistentFlags().String("db", dbPath, "")
	root.PersistentFlags().Bool("json", false, "")
	root.AddCommand(NewAddCmd())
	root.SetArgs([]string{"add", "feature work", "--base-branch", "feature/source"})
	root.SetOut(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	tasks, err := database.ListTasks("proposed", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].BaseBranch == nil || *tasks[0].BaseBranch != "feature/source" {
		t.Fatalf("stored task = %+v, want base branch feature/source", tasks)
	}
}
