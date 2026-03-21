package db

import (
	"os"
	"path/filepath"
	"testing"
)

func tempDBPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	return filepath.Join(dir, "sub", "test.db")
}

func TestOpenCreatesFile(t *testing.T) {
	path := tempDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestWALMode(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	var mode string
	if err := d.q.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}
}

func TestForeignKeysOn(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	var fk int
	if err := d.q.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatalf("query foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("expected foreign_keys=1, got %d", fk)
	}
}

func TestTablesExist(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tables := []string{"tasks", "deps", "notes"}
	for _, tbl := range tables {
		var name string
		err := d.q.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", tbl,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", tbl, err)
		}
	}
}

func TestBeginTxCommit(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	_, err = tx.q.Exec("INSERT INTO tasks (id, title) VALUES ('t001', 'test task')")
	if err != nil {
		t.Fatalf("insert in tx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	var title string
	if err := d.q.QueryRow("SELECT title FROM tasks WHERE id='t001'").Scan(&title); err != nil {
		t.Fatalf("query after commit: %v", err)
	}
	if title != "test task" {
		t.Errorf("expected 'test task', got %q", title)
	}
}

func TestBeginTxRollback(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	tx, err := d.BeginTx()
	if err != nil {
		t.Fatalf("BeginTx failed: %v", err)
	}

	_, err = tx.q.Exec("INSERT INTO tasks (id, title) VALUES ('t002', 'rollback task')")
	if err != nil {
		t.Fatalf("insert in tx: %v", err)
	}

	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}

	var count int
	if err := d.q.QueryRow("SELECT COUNT(*) FROM tasks WHERE id='t002'").Scan(&count); err != nil {
		t.Fatalf("query after rollback: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 rows after rollback, got %d", count)
	}
}

func TestAddTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("my task", "some desc", "", "")
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	if len(task.ID) != 4 {
		t.Errorf("expected 4-char ID, got %q (len %d)", task.ID, len(task.ID))
	}
	if task.Title != "my task" {
		t.Errorf("expected title 'my task', got %q", task.Title)
	}
	if task.Description != "some desc" {
		t.Errorf("expected description 'some desc', got %q", task.Description)
	}
	if task.Status != "open" {
		t.Errorf("expected status 'open', got %q", task.Status)
	}
}

func TestAddTask_WithParent(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	parent, err := d.AddTask("parent", "", "", "")
	if err != nil {
		t.Fatalf("AddTask parent failed: %v", err)
	}

	child, err := d.AddTask("child", "", parent.ID, "")
	if err != nil {
		t.Fatalf("AddTask child failed: %v", err)
	}

	if child.ParentID == nil {
		t.Fatal("expected parent_id to be set")
	}
	if *child.ParentID != parent.ID {
		t.Errorf("expected parent_id %q, got %q", parent.ID, *child.ParentID)
	}
}

func TestAddTask_WithAfter(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	blocker, err := d.AddTask("blocker", "", "", "")
	if err != nil {
		t.Fatalf("AddTask blocker failed: %v", err)
	}

	blocked, err := d.AddTask("blocked", "", "", blocker.ID)
	if err != nil {
		t.Fatalf("AddTask blocked failed: %v", err)
	}

	blockers, err := d.GetBlockers(blocked.ID)
	if err != nil {
		t.Fatalf("GetBlockers failed: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(blockers))
	}
	if blockers[0].ID != blocker.ID {
		t.Errorf("expected blocker ID %q, got %q", blocker.ID, blockers[0].ID)
	}
}

func TestGetTask_NotFound(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	_, err = d.GetTask("xxxx")
	if err == nil {
		t.Fatal("expected error for non-existent task")
	}
}

func TestAddDep(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, err := d.AddTask("task A", "", "", "")
	if err != nil {
		t.Fatalf("AddTask A: %v", err)
	}
	b, err := d.AddTask("task B", "", "", "")
	if err != nil {
		t.Fatalf("AddTask B: %v", err)
	}

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("AddDep: %v", err)
	}

	blockers, err := d.GetBlockers(b.ID)
	if err != nil {
		t.Fatalf("GetBlockers: %v", err)
	}
	if len(blockers) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(blockers))
	}
	if blockers[0].ID != a.ID {
		t.Errorf("expected blocker %q, got %q", a.ID, blockers[0].ID)
	}
}

func TestAddDep_CycleDetection(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "")
	b, _ := d.AddTask("B", "", "", "")
	c, _ := d.AddTask("C", "", "", "")

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("A->B: %v", err)
	}
	if err := d.AddDep(b.ID, c.ID); err != nil {
		t.Fatalf("B->C: %v", err)
	}

	// C->A should create a cycle
	if err := d.AddDep(c.ID, a.ID); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestAddDep_SelfCycle(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "")

	if err := d.AddDep(a.ID, a.ID); err == nil {
		t.Fatal("expected self-cycle error, got nil")
	}
}

func TestRemoveDep(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "")
	b, _ := d.AddTask("B", "", "", "")

	if err := d.AddDep(a.ID, b.ID); err != nil {
		t.Fatalf("AddDep: %v", err)
	}

	if err := d.RemoveDep(a.ID, b.ID); err != nil {
		t.Fatalf("RemoveDep: %v", err)
	}

	blockers, err := d.GetBlockers(b.ID)
	if err != nil {
		t.Fatalf("GetBlockers: %v", err)
	}
	if len(blockers) != 0 {
		t.Errorf("expected 0 blockers after remove, got %d", len(blockers))
	}
}

func TestRemoveDep_NotFound(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	if err := d.RemoveDep("xxxx", "yyyy"); err == nil {
		t.Fatal("expected error for non-existent dep")
	}
}

func TestGetBlocking(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	a, _ := d.AddTask("A", "", "", "")
	b, _ := d.AddTask("B", "", "", "")
	c, _ := d.AddTask("C", "", "", "")

	// A blocks B and C
	d.AddDep(a.ID, b.ID)
	d.AddDep(a.ID, c.ID)

	blocking, err := d.GetBlocking(a.ID)
	if err != nil {
		t.Fatalf("GetBlocking: %v", err)
	}
	if len(blocking) != 2 {
		t.Fatalf("expected 2 tasks blocked by A, got %d", len(blocking))
	}
}

func TestEditTask(t *testing.T) {
	d, err := Open(tempDBPath(t))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer d.Close()

	task, err := d.AddTask("original", "original desc", "", "")
	if err != nil {
		t.Fatalf("AddTask failed: %v", err)
	}

	newTitle := "updated"
	updated, err := d.EditTask(task.ID, &newTitle, nil)
	if err != nil {
		t.Fatalf("EditTask failed: %v", err)
	}

	if updated.Title != "updated" {
		t.Errorf("expected title 'updated', got %q", updated.Title)
	}
	if updated.Description != "original desc" {
		t.Errorf("expected description unchanged, got %q", updated.Description)
	}
}
