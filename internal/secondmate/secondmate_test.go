package secondmate

import (
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestInvestigatorUsesOnlyConfirmedEmptyDiffRecovery(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	auto, err := d.AddTask("empty PR", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.BlockTask(auto.ID, "pr: gh pr create: exit status 1\npull request create failed: GraphQL: No commits between main and dispatch/plan-8a84 (createPullRequest)"); err != nil {
		t.Fatal(err)
	}

	conflict, err := d.AddTask("conflict", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.BlockTask(conflict.ID, "merge conflict merging into plan branch"); err != nil {
		t.Fatal(err)
	}

	results, err := (&Investigator{DB: d}).Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Action != ActionReopen || results[1].Action != ActionPresentOptions ||
		results[1].Classification != ClassificationNotAutoUnblockable {
		t.Fatalf("results = %+v", results)
	}
	got, err := d.GetTask(auto.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" {
		t.Fatalf("auto task status = %s", got.Status)
	}
	got, err = d.GetTask(conflict.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "blocked" {
		t.Fatalf("conflict task status = %s", got.Status)
	}
}

func TestInvestigatorDurablyLimitsRetries(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.AddTask("missing prerequisite", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.BlockTask(task.ID, "missing prerequisite: dt go --thought-file is not installed"); err != nil {
		t.Fatal(err)
	}
	investigator := &Investigator{DB: d}
	if _, err := investigator.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := investigator.Run(); err != nil {
		t.Fatal(err)
	}
	if _, err := investigator.Run(); err != nil {
		t.Fatal(err)
	}
	rows, err := d.SecondmateInvestigations(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[2].Action != ActionSkipRetry || rows[2].RetryCount != 2 {
		t.Fatalf("rows = %+v", rows)
	}
}
