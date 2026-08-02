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

	// busy_timeout and foreign_keys are per-connection and non-persistent, so
	// they must be in the DSN — a PRAGMA exec only configures whichever pooled
	// connection happens to run it. journal_mode=WAL is persisted in the file.
	sqlDB, err := sql.Open("sqlite3", path+"?_busy_timeout=5000&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	pragmas := []string{
		"PRAGMA journal_mode=WAL",
	}
	for _, p := range pragmas {
		if _, err := sqlDB.Exec(p); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("pragma %q: %w", p, err)
		}
	}

	d := &DB{q: sqlDB, sqlDB: sqlDB}
	// Pin the pool to one connection during migration: the rebuild path issues
	// BEGIN/COMMIT and per-connection pragmas as separate Execs, which must all
	// land on the same connection.
	sqlDB.SetMaxOpenConns(1)
	err = d.migrate()
	sqlDB.SetMaxOpenConns(0)
	if err != nil {
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

// taskColumnsV2 is the full v2 tasks schema. Fresh databases are created with
// it directly; legacy databases are rebuilt into it (SQLite cannot alter a
// CHECK constraint in place).
const taskSchemaV2 = `(
	id          TEXT PRIMARY KEY,
	title       TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	status      TEXT NOT NULL DEFAULT 'open'
	            CHECK (status IN ('open','active','blocked','done',
	                              'live','unattended','parked','killed','proposed')),
	block_reason TEXT,
	assignee    TEXT,
	parent_id   TEXT REFERENCES tasks(id),
	created_at  TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at  TEXT NOT NULL DEFAULT (datetime('now')),
	repo        TEXT,
	thought     TEXT NOT NULL DEFAULT '',
	label       TEXT,
	mode        TEXT,
	workdir     TEXT,
	herdr_ws    TEXT,
	herdr_tab   TEXT,
	herdr_pane  TEXT,
	held_by     TEXT,
	held_pid    INTEGER,
	acceptance_kind TEXT,
	acceptance  TEXT,
	kill_reason TEXT,
	last_activity TEXT,
	reject_count INTEGER NOT NULL DEFAULT 0,
	reviewing     INTEGER NOT NULL DEFAULT 0
)`

const updatedAtTrigger = `CREATE TRIGGER IF NOT EXISTS tasks_updated_at
	AFTER UPDATE ON tasks
	WHEN NEW.updated_at = OLD.updated_at
	BEGIN
		UPDATE tasks SET updated_at = datetime('now') WHERE id = OLD.id;
	END`

func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks ` + taskSchemaV2,
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
		`CREATE TABLE IF NOT EXISTS meta (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		updatedAtTrigger,
	}
	for _, s := range stmts {
		if _, err := d.q.Exec(s); err != nil {
			return fmt.Errorf("exec migration: %w\nSQL: %s", err, s)
		}
	}

	// Legacy databases predate the v2 columns and carry a status CHECK that
	// rejects the v2 statuses. SQLite can't alter a CHECK, so rebuild the
	// table once: copy rows into a fresh v2-schema table and swap it in.
	hasThought, err := d.hasTaskColumn("thought")
	if err != nil {
		return err
	}
	if hasThought {
		hasReviewing, err := d.hasTaskColumn("reviewing")
		if err != nil {
			return err
		}
		if !hasReviewing {
			if _, err := d.q.Exec("ALTER TABLE tasks ADD COLUMN reviewing INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add reviewing column: %w", err)
			}
		}
		return nil
	}

	hasRepo, err := d.hasTaskColumn("repo")
	if err != nil {
		return err
	}
	repoExpr := "repo"
	if !hasRepo {
		repoExpr = "NULL"
	}

	// Foreign keys must be off for the drop/rename swap; deps and notes still
	// reference tasks. The pragma is a no-op inside a transaction, so it
	// brackets one.
	if _, err := d.q.Exec("PRAGMA foreign_keys=OFF"); err != nil {
		return fmt.Errorf("fk off: %w", err)
	}
	rebuild := []string{
		"BEGIN",
		`CREATE TABLE tasks_v2_migration ` + taskSchemaV2,
		`INSERT INTO tasks_v2_migration
			(id, title, description, status, block_reason, assignee, parent_id,
			 created_at, updated_at, repo)
		 SELECT id, title, description, status, block_reason, assignee, parent_id,
			 created_at, updated_at, ` + repoExpr + ` FROM tasks`,
		"DROP TABLE tasks",
		"ALTER TABLE tasks_v2_migration RENAME TO tasks",
		updatedAtTrigger,
		"COMMIT",
	}
	for _, s := range rebuild {
		if _, err := d.q.Exec(s); err != nil {
			d.q.Exec("ROLLBACK")
			d.q.Exec("PRAGMA foreign_keys=ON")
			return fmt.Errorf("rebuild tasks table: %w\nSQL: %s", err, s)
		}
	}
	if _, err := d.q.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("fk on: %w", err)
	}
	return nil
}

func (d *DB) hasTaskColumn(col string) (bool, error) {
	rows, err := d.q.Query("PRAGMA table_info(tasks)")
	if err != nil {
		return false, fmt.Errorf("pragma table_info: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt *string
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, fmt.Errorf("scan table_info: %w", err)
		}
		if name == col {
			return true, nil
		}
	}
	return false, nil
}
