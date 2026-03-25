package commands

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
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

func TestFindPlanParent(t *testing.T) {
	t.Run("parent first", func(t *testing.T) {
		d := openTestDB(t)

		parent, err := d.AddTask("Parent", "", "", "", nil)
		if err != nil {
			t.Fatalf("add parent: %v", err)
		}
		child1, err := d.AddTask("Child1", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child1: %v", err)
		}
		child2, err := d.AddTask("Child2", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child2: %v", err)
		}

		refs := []string{parent.ID, child1.ID, child2.ID}
		got := findPlanParent(d, refs)
		if got != parent.ID {
			t.Errorf("findPlanParent (parent first) = %q, want %q", got, parent.ID)
		}
	})

	t.Run("parent last", func(t *testing.T) {
		d := openTestDB(t)

		parent, err := d.AddTask("Parent", "", "", "", nil)
		if err != nil {
			t.Fatalf("add parent: %v", err)
		}
		child1, err := d.AddTask("Child1", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child1: %v", err)
		}
		child2, err := d.AddTask("Child2", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child2: %v", err)
		}

		refs := []string{child1.ID, child2.ID, parent.ID}
		got := findPlanParent(d, refs)
		if got != parent.ID {
			t.Errorf("findPlanParent (parent last) = %q, want %q", got, parent.ID)
		}
	})

	t.Run("parent middle", func(t *testing.T) {
		d := openTestDB(t)

		parent, err := d.AddTask("Parent", "", "", "", nil)
		if err != nil {
			t.Fatalf("add parent: %v", err)
		}
		child1, err := d.AddTask("Child1", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child1: %v", err)
		}
		child2, err := d.AddTask("Child2", "", parent.ID, "", nil)
		if err != nil {
			t.Fatalf("add child2: %v", err)
		}

		refs := []string{child1.ID, parent.ID, child2.ID}
		got := findPlanParent(d, refs)
		if got != parent.ID {
			t.Errorf("findPlanParent (parent middle) = %q, want %q", got, parent.ID)
		}
	})

	t.Run("empty refs", func(t *testing.T) {
		d := openTestDB(t)

		got := findPlanParent(d, []string{})
		if got != "" {
			t.Errorf("findPlanParent (empty refs) = %q, want \"\"", got)
		}
	})

	t.Run("orphan tasks only", func(t *testing.T) {
		d := openTestDB(t)

		// Two unrelated tasks with no parent-child relationship.
		t1, err := d.AddTask("Task1", "", "", "", nil)
		if err != nil {
			t.Fatalf("add task1: %v", err)
		}
		t2, err := d.AddTask("Task2", "", "", "", nil)
		if err != nil {
			t.Fatalf("add task2: %v", err)
		}

		refs := []string{t1.ID, t2.ID}
		got := findPlanParent(d, refs)
		if got != "" {
			t.Errorf("findPlanParent (orphan tasks) = %q, want \"\"", got)
		}
	})

	t.Run("single task", func(t *testing.T) {
		d := openTestDB(t)

		task, err := d.AddTask("Lone", "", "", "", nil)
		if err != nil {
			t.Fatalf("add task: %v", err)
		}

		refs := []string{task.ID}
		got := findPlanParent(d, refs)
		if got != "" {
			t.Errorf("findPlanParent (single task) = %q, want \"\"", got)
		}
	})
}

func TestMaybeWireGP_NoEnvVar(t *testing.T) {
	// When GRAPHPILOT_NODE is not set (empty string), maybeWireGP must not
	// attempt any GP command — verified by the absence of any output on stderr.
	d := openTestDB(t)

	parent, err := d.AddTask("Parent", "", "", "", nil)
	if err != nil {
		t.Fatalf("add parent: %v", err)
	}
	child, err := d.AddTask("Child", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("add child: %v", err)
	}

	var buf bytes.Buffer
	maybeWireGP(d, []string{parent.ID, child.ID}, "" /* gpNode not set */, &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no stderr output when GRAPHPILOT_NODE is empty, got: %q", buf.String())
	}
}

func TestMaybeWireGP_NoParentInRefs(t *testing.T) {
	// When GRAPHPILOT_NODE is set but no parent-child relationship exists among
	// the batch refs, the warning "no plan parent found" must appear on stderr.
	d := openTestDB(t)

	t1, err := d.AddTask("Task1", "", "", "", nil)
	if err != nil {
		t.Fatalf("add task1: %v", err)
	}
	t2, err := d.AddTask("Task2", "", "", "", nil)
	if err != nil {
		t.Fatalf("add task2: %v", err)
	}

	var buf bytes.Buffer
	maybeWireGP(d, []string{t1.ID, t2.ID}, "gp-node-abc", &buf)

	got := buf.String()
	if !strings.Contains(got, "no plan parent found") {
		t.Errorf("expected warning about no plan parent, got: %q", got)
	}
}

func TestMaybeWireGP_EmptyRefs(t *testing.T) {
	// When GRAPHPILOT_NODE is set but refs is empty, no output is expected
	// (there's nothing to wire and no misleading warning needed).
	d := openTestDB(t)

	var buf bytes.Buffer
	maybeWireGP(d, []string{}, "gp-node-abc", &buf)

	if buf.Len() != 0 {
		t.Errorf("expected no stderr output for empty refs, got: %q", buf.String())
	}
}

func TestMaybeWireGP_GPNotInPath(t *testing.T) {
	// When GRAPHPILOT_NODE is set and a plan parent exists but `gp` is not in
	// PATH, the "gp not in PATH" warning must appear on stderr.
	// This test relies on the test environment not having a `gp` binary.
	// If `gp` is present, the test is skipped to avoid side effects.
	if _, err := exec.LookPath("gp"); err == nil {
		t.Skip("gp binary is present in PATH; skipping test that expects its absence")
	}

	d := openTestDB(t)

	parent, err := d.AddTask("Parent", "", "", "", nil)
	if err != nil {
		t.Fatalf("add parent: %v", err)
	}
	child, err := d.AddTask("Child", "", parent.ID, "", nil)
	if err != nil {
		t.Fatalf("add child: %v", err)
	}

	var buf bytes.Buffer
	maybeWireGP(d, []string{parent.ID, child.ID}, "gp-node-xyz", &buf)

	got := buf.String()
	if !strings.Contains(got, "gp not in PATH") {
		t.Errorf("expected warning about gp not in PATH, got: %q", got)
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
