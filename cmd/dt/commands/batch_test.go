package commands

import (
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	d, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestBatchBackReferences(t *testing.T) {
	d := openTestDB(t)

	// Simulate batch input:
	//   add "Parent" -d "parent task"
	//   add "Child" -d "child" -p $1
	//   dep $2 $1   (child depends on parent)
	lines := []string{
		`add "Parent" -d "parent task"`,
		`add "Child" -d "child" -p $1`,
		`dep $2 $1`,
	}

	refs := []string{}
	for i, line := range lines {
		resolved, err := substituteRefs(line, refs)
		if err != nil {
			t.Fatalf("line %d: substituteRefs: %v", i+1, err)
		}
		id, err := executeLine(d, resolved)
		if err != nil {
			t.Fatalf("line %d: executeLine: %v", i+1, err)
		}
		if id != "" {
			refs = append(refs, id)
		}
	}

	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}

	parentID := refs[0]
	childID := refs[1]

	// Verify parent task.
	parent, err := d.GetTask(parentID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if parent.Title != "Parent" {
		t.Errorf("parent title: got %q, want %q", parent.Title, "Parent")
	}
	if parent.ParentID != nil {
		t.Errorf("parent should have no parent_id, got %v", *parent.ParentID)
	}

	// Verify child task has correct parent.
	child, err := d.GetTask(childID)
	if err != nil {
		t.Fatalf("get child: %v", err)
	}
	if child.Title != "Child" {
		t.Errorf("child title: got %q, want %q", child.Title, "Child")
	}
	if child.ParentID == nil || *child.ParentID != parentID {
		t.Errorf("child parent_id: got %v, want %q", child.ParentID, parentID)
	}

	// Verify dependency: parent blocks child.
	blockers, err := d.GetBlockers(childID)
	if err != nil {
		t.Fatalf("get blockers: %v", err)
	}
	if len(blockers) != 1 || blockers[0].ID != parentID {
		t.Errorf("expected child blocked by parent, got blockers: %v", blockers)
	}
}

func TestBatchBackReference_InvalidIndex(t *testing.T) {
	// $99 when only 1 task exists
	_, err := substituteRefs(`add "x" -p $99`, []string{"abc123"})
	if err == nil {
		t.Fatal("expected error for $99 with only 1 ref")
	}

	// $0 is invalid (1-indexed)
	_, err = substituteRefs(`add "x" -p $0`, []string{"abc123"})
	if err == nil {
		t.Fatal("expected error for $0 (1-indexed)")
	}
}

func TestBatchMultilineDescription(t *testing.T) {
	d := openTestDB(t)

	// Use executeLine directly with a pre-joined multiline arg.
	multiline := "add \"Parent\" -d \"line one\nline two\nline three\""
	id, err := executeLine(d, multiline)
	if err != nil {
		t.Fatalf("executeLine multiline: %v", err)
	}
	if id == "" {
		t.Fatal("expected non-empty id")
	}

	task, err := d.GetTask(id)
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	want := "line one\nline two\nline three"
	if task.Description != want {
		t.Errorf("description = %q, want %q", task.Description, want)
	}
}

func TestQuotesBalanced(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`add "complete"`, true},
		{`add "incomplete`, false},
		{`add "one" -d "two"`, true},
		{`add 'single'`, true},
		{`add 'open`, false},
		{`add "has 'inner' quotes"`, true},
	}
	for _, tt := range tests {
		got := quotesBalanced(tt.input)
		if got != tt.want {
			t.Errorf("quotesBalanced(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestSubstituteRefs(t *testing.T) {
	refs := []string{"id-aaa", "id-bbb", "id-ccc"}

	tests := []struct {
		input string
		want  string
	}{
		{`dep $1 $2`, `dep id-aaa id-bbb`},
		{`add "x" -p $3`, `add "x" -p id-ccc`},
		{`add "no refs here"`, `add "no refs here"`},
		{`dep $1 $3`, `dep id-aaa id-ccc`},
	}

	for _, tt := range tests {
		got, err := substituteRefs(tt.input, refs)
		if err != nil {
			t.Errorf("substituteRefs(%q): %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("substituteRefs(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
