package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

// queryable is satisfied by both *sql.DB and *sql.Tx, allowing all DB
// methods to work in regular and transactional contexts.
type queryable interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DB wraps a SQLite database with support for transactions.
type DB struct {
	q     queryable
	sqlDB *sql.DB
}

// Open creates the directory for path if needed, opens a SQLite database,
// configures pragmas, and runs migrations.
func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	sqlDB, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Set pragmas.
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	d := &DB{q: sqlDB, sqlDB: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return d, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.sqlDB.Close()
}

// BeginTx starts a transaction and returns a new DB whose queryable is the tx.
func (d *DB) BeginTx() (*DB, error) {
	tx, err := d.sqlDB.Begin()
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	return &DB{q: tx, sqlDB: d.sqlDB}, nil
}

// Commit commits the transaction. Panics if q is not a *sql.Tx.
func (d *DB) Commit() error {
	tx, ok := d.q.(*sql.Tx)
	if !ok {
		return fmt.Errorf("Commit called on non-transactional DB")
	}
	return tx.Commit()
}

// Rollback rolls back the transaction. Panics if q is not a *sql.Tx.
func (d *DB) Rollback() error {
	tx, ok := d.q.(*sql.Tx)
	if !ok {
		return fmt.Errorf("Rollback called on non-transactional DB")
	}
	return tx.Rollback()
}

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			id          TEXT PRIMARY KEY,
			title       TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			status      TEXT NOT NULL DEFAULT 'open'
			            CHECK (status IN ('open','active','blocked','done')),
			block_reason TEXT,
			assignee    TEXT,
			parent_id   TEXT REFERENCES tasks(id),
			created_at  TEXT NOT NULL DEFAULT (datetime('now')),
			updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TABLE IF NOT EXISTS deps (
			blocker_id TEXT NOT NULL REFERENCES tasks(id),
			blocked_id TEXT NOT NULL REFERENCES tasks(id),
			PRIMARY KEY (blocker_id, blocked_id)
		)`,
		`CREATE TABLE IF NOT EXISTS notes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id    TEXT NOT NULL REFERENCES tasks(id),
			content    TEXT NOT NULL,
			author     TEXT,
			created_at TEXT NOT NULL DEFAULT (datetime('now'))
		)`,
		`CREATE TRIGGER IF NOT EXISTS tasks_updated_at
		AFTER UPDATE ON tasks
		WHEN NEW.updated_at = OLD.updated_at
		BEGIN
			UPDATE tasks SET updated_at = datetime('now') WHERE id = OLD.id;
		END`,
	}
	for _, s := range stmts {
		if _, err := d.q.Exec(s); err != nil {
			return fmt.Errorf("exec migration: %w\nSQL: %s", err, s)
		}
	}
	return nil
}
