package commands

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func TestUsageCommandHumanAndJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	task, err := d.AddTask("usage", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.StartAttempt("a1", task.ID, "worker", "codex", nil); err != nil {
		t.Fatal(err)
	}
	in, out, turns, status := int64(12), int64(3), 1, 0
	if err := d.FinishAttempt("a1", db.Attempt{InputTokens: &in, OutputTokens: &out, TurnCount: &turns, ExitStatus: &status}); err != nil {
		t.Fatal(err)
	}
	d.Close()

	for _, tc := range []struct {
		name, args, want string
	}{
		{"human", "usage " + task.ID, "TOTAL attempts=1 input=12"},
		{"json", "--json usage " + task.ID, `"input_tokens": 12`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := &cobra.Command{Use: "dt"}
			root.PersistentFlags().String("db", path, "database")
			root.PersistentFlags().Bool("json", false, "json")
			root.AddCommand(NewUsageCmd())
			root.SetArgs(strings.Fields(tc.args))
			var got bytes.Buffer
			root.SetOut(&got)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			if tc.name == "json" {
				var report db.UsageReport
				if err := json.Unmarshal(got.Bytes(), &report); err != nil {
					t.Fatal(err)
				}
				if report.Totals.InputTokens != 12 {
					t.Fatalf("report = %+v", report.Totals)
				}
			} else if !strings.Contains(got.String(), tc.want) {
				t.Fatalf("output = %q", got.String())
			}
		})
	}
}
