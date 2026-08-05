package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// openLegacyDB creates a database with the pre-v2 schema and some rows, then
// closes it, simulating an existing installation.
func openLegacyDB(t *testing.T, path string) {
	t.Helper()
	raw, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	stmts := []string{
		`CREATE TABLE tasks (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'open'
				CHECK (status IN ('open','active','blocked','done')),
			block_reason TEXT,
			assignee TEXT,
			parent_id TEXT REFERENCES tasks(id),
			created_at TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			repo TEXT
		)`,
		`CREATE TABLE deps (
			blocker_id TEXT NOT NULL REFERENCES tasks(id),
			blocked_id TEXT NOT NULL REFERENCES tasks(id),
			PRIMARY KEY (blocker_id, blocked_id)
		)`,
		`CREATE TABLE notes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id TEXT NOT NULL REFERENCES tasks(id),
			content TEXT NOT NULL,
			author TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`INSERT INTO tasks (id, title, status, repo) VALUES
			('aa11', 'legacy open', 'open', 'sc-api'),
			('bb22', 'legacy done', 'done', NULL)`,
		`INSERT INTO deps VALUES ('bb22', 'aa11')`,
		`INSERT INTO notes (task_id, content) VALUES ('aa11', 'a note')`,
	}
	for _, s := range stmts {
		if _, err := raw.Exec(s); err != nil {
			t.Fatalf("legacy setup: %v\n%s", err, s)
		}
	}
}

func TestMigrateLegacyToV2(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	openLegacyDB(t, path)

	d, err := Open(path)
	if err != nil {
		t.Fatalf("open over legacy db: %v", err)
	}
	defer d.Close()

	// Legacy rows survive with their deps and notes intact.
	legacy, err := d.GetTaskV2("aa11")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Title != "legacy open" || legacy.Status != "open" || *legacy.Repo != "sc-api" {
		t.Errorf("legacy row mangled: %+v", legacy)
	}
	notes, err := d.GetNotes("aa11")
	if err != nil || len(notes) != 1 {
		t.Errorf("notes lost in migration: %v, %v", notes, err)
	}

	// The rebuilt table accepts v2 statuses.
	captured, err := d.CaptureTask("i want a viewer for this data", "sc-api", "worktree")
	if err != nil {
		t.Fatal(err)
	}
	if captured.Status != "live" || captured.Thought != "i want a viewer for this data" {
		t.Errorf("capture wrong: %+v", captured)
	}
	if *captured.Label != "i want a viewer" {
		t.Errorf("label truncation wrong: %q", *captured.Label)
	}

	// Re-opening does not attempt a second rebuild.
	d.Close()
	d2, err := Open(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer d2.Close()
	if _, err := d2.q.Exec("UPDATE tasks SET status = 'open' WHERE id = ?", captured.ID); err != nil {
		t.Fatal(err)
	}
	repaired, err := d2.ResumeTask(captured.ID)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Status != "live" {
		t.Errorf("released capture task status = %q, want live", repaired.Status)
	}

	// Lifecycle: park/resume/kill.
	if _, err := d2.ParkTask(captured.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d2.KillTask(captured.ID, ""); err == nil {
		t.Error("kill without reason must fail")
	}
	if _, err := d2.ResumeTask(captured.ID); err != nil {
		t.Fatal(err)
	}
	killed, err := d2.KillTask(captured.ID, "superseded")
	if err != nil {
		t.Fatal(err)
	}
	if killed.Status != "killed" || *killed.KillReason != "superseded" {
		t.Errorf("kill wrong: %+v", killed)
	}

	// Board shows daemon-managed legacy work as well as v2 work.
	if _, err := d2.ClaimTask("aa11", "dispatchd-aa11"); err != nil {
		t.Fatal(err)
	}
	board, err := d2.BoardTasks()
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]bool, len(board))
	for _, task := range board {
		ids[task.ID] = true
	}
	if len(board) != 2 || !ids[captured.ID] || !ids["aa11"] {
		t.Errorf("board wrong: %+v", board)
	}
}
