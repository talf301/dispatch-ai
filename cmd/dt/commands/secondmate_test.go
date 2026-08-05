package commands

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func TestSecondmateCommandReportsInvestigations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dispatch.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.AddTask("empty diff", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.BlockTaskWithKind(task.ID, "pr: gh pr create: exit status 1\npull request create failed: No commits between main and branch", db.BlockKindPRCreateFailed); err != nil {
		t.Fatal(err)
	}
	d.Close()

	root := &cobra.Command{Use: "dt"}
	root.PersistentFlags().String("db", path, "database")
	root.PersistentFlags().Bool("json", false, "json")
	root.AddCommand(NewSecondmateCmd())
	root.SetArgs(strings.Fields("secondmate"))
	var output bytes.Buffer
	root.SetOut(&output)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), task.ID+"\tauto-unblockable\treopen") {
		t.Fatalf("output = %q", output.String())
	}
}
