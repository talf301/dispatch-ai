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
