package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/id"
)

// v2 (capture-first) task operations. A captured task is created `live` with
// the verbatim thought; the label is a display cache seeded by truncation.

// TruncateLabel derives the initial display label: the first four words of
// the thought, capped. Model-generated labels replace it later (M2).
func TruncateLabel(thought string) string {
	words := strings.Fields(thought)
	if len(words) > 4 {
		words = words[:4]
	}
	label := strings.Join(words, " ")
	if len(label) > 32 {
		label = label[:31] + "…"
	}
	return label
}

const taskColumnsV2 = `id, title, description, status, block_reason, assignee,
	parent_id, repo, agent, created_at, updated_at,
	thought, label, mode, workdir, herdr_ws, herdr_tab, herdr_pane,
	kill_reason, last_activity, acceptance_kind, acceptance, reject_count`

func (d *DB) scanTaskV2(row interface{ Scan(...any) error }) (*Task, error) {
	t := &Task{}
	err := row.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.BlockReason,
		&t.Assignee, &t.ParentID, &t.Repo, &t.Agent, &t.CreatedAt, &t.UpdatedAt,
		&t.Thought, &t.Label, &t.Mode, &t.Workdir, &t.HerdrWs, &t.HerdrTab,
		&t.HerdrPane, &t.KillReason, &t.LastActivity,
		&t.AcceptanceKind, &t.Acceptance, &t.RejectCount)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// CaptureTask inserts a live task from a verbatim thought. mode is
// "worktree" or "in_place". The herdr coordinates are filled in afterwards
// with SetRuntime, once the tab exists.
func (d *DB) CaptureTask(thought, repo, mode string) (*Task, error) {
	return d.CaptureTaskWithAgent(thought, repo, mode, nil)
}

func (d *DB) CaptureTaskWithAgent(thought, repo, mode string, agent *string) (*Task, error) {
	if strings.TrimSpace(thought) == "" {
		return nil, fmt.Errorf("thought must not be empty")
	}
	taskID, err := d.newTaskID()
	if err != nil {
		return nil, err
	}
	label := TruncateLabel(thought)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = d.q.Exec(
		`INSERT INTO tasks (id, title, status, repo, agent, thought, label, mode,
			created_at, updated_at, last_activity)
		 VALUES (?, ?, 'live', ?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, label, repo, agent, thought, label, mode, now, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert captured task: %w", err)
	}
	return d.GetTaskV2(taskID)
}

// SetRuntime records where a task runs: its working directory and herdr
// coordinates.
func (d *DB) SetRuntime(taskID, workdir, ws, tab, pane string) error {
	_, err := d.q.Exec(
		`UPDATE tasks SET workdir = ?, herdr_ws = ?, herdr_tab = ?, herdr_pane = ?
		 WHERE id = ?`,
		workdir, ws, tab, pane, taskID,
	)
	if err != nil {
		return fmt.Errorf("set runtime: %w", err)
	}
	return nil
}

// GetTaskV2 retrieves a task including the v2 columns.
func (d *DB) GetTaskV2(taskID string) (*Task, error) {
	t, err := d.scanTaskV2(d.q.QueryRow(
		`SELECT `+taskColumnsV2+` FROM tasks WHERE id = ?`, taskID))
	if err != nil {
		return nil, fmt.Errorf("get task %q: %w", taskID, err)
	}
	return t, nil
}

// SetLabel updates the display label. The label is a cache: it never feeds
// retrieval or matching — thought does.
func (d *DB) SetLabel(taskID, label string) error {
	if strings.TrimSpace(label) == "" {
		return fmt.Errorf("label must not be empty")
	}
	if _, err := d.q.Exec("UPDATE tasks SET label = ? WHERE id = ?", label, taskID); err != nil {
		return fmt.Errorf("set label: %w", err)
	}
	return nil
}

// DeleteTask removes a task row outright. Only for rolling back a capture
// that failed at birth — closed work is killed, never deleted.
func (d *DB) DeleteTask(taskID string) error {
	if _, err := d.q.Exec("DELETE FROM tasks WHERE id = ?", taskID); err != nil {
		return fmt.Errorf("delete task: %w", err)
	}
	return nil
}

// KillTask closes a task without completion. The reason is mandatory.
func (d *DB) KillTask(taskID, reason string) (*Task, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("kill requires a reason")
	}
	t, err := d.GetTaskV2(taskID)
	if err != nil {
		return nil, err
	}
	if t.Status == "done" || t.Status == "killed" {
		return nil, fmt.Errorf("task %s is already %s", taskID, t.Status)
	}
	_, err = d.q.Exec(
		"UPDATE tasks SET status = 'killed', kill_reason = ? WHERE id = ?",
		reason, taskID)
	if err != nil {
		return nil, fmt.Errorf("kill task: %w", err)
	}
	if err := d.addSystemNote(taskID, t.Status, "killed"); err != nil {
		return nil, err
	}
	return d.GetTaskV2(taskID)
}

// ParkTask shelves a live task; the worktree is preserved.
func (d *DB) ParkTask(taskID string) (*Task, error) {
	return d.transition(taskID, "live", "parked")
}

// ResumeTask brings a parked task back to live.
func (d *DB) ResumeTask(taskID string) (*Task, error) {
	return d.transition(taskID, "parked", "live")
}

func (d *DB) transition(taskID, from, to string) (*Task, error) {
	t, err := d.GetTaskV2(taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != from {
		return nil, fmt.Errorf("task %s is %s, not %s", taskID, t.Status, from)
	}
	if _, err := d.q.Exec("UPDATE tasks SET status = ? WHERE id = ?", to, taskID); err != nil {
		return nil, fmt.Errorf("update status: %w", err)
	}
	if err := d.addSystemNote(taskID, from, to); err != nil {
		return nil, err
	}
	return d.GetTaskV2(taskID)
}

// PromoteTask moves a live task to unattended — the only gated transition on
// the human path. The written acceptance is the entire ceremony budget:
// without one, the daemon has nothing to gate the merge on, so this refuses.
// In-place tasks can never be promoted (they'd auto-merge a dirty tree).
func (d *DB) PromoteTask(taskID, kind, acceptance string) (*Task, error) {
	if kind != "report" && kind != "ratchet" {
		return nil, fmt.Errorf("acceptance kind must be report or ratchet, got %q", kind)
	}
	if strings.TrimSpace(acceptance) == "" {
		return nil, fmt.Errorf("promote requires an acceptance condition")
	}
	t, err := d.GetTaskV2(taskID)
	if err != nil {
		return nil, err
	}
	if t.Status != "live" {
		return nil, fmt.Errorf("task %s is %s; only live tasks promote", taskID, t.Status)
	}
	if t.Mode == nil || *t.Mode != "worktree" {
		return nil, fmt.Errorf("task %s runs in place; in-place work cannot be promoted (no branch to merge)", taskID)
	}
	_, err = d.q.Exec(
		`UPDATE tasks SET status = 'unattended', acceptance_kind = ?, acceptance = ?,
		        reject_count = 0
		 WHERE id = ?`, kind, acceptance, taskID)
	if err != nil {
		return nil, fmt.Errorf("promote: %w", err)
	}
	if err := d.addSystemNote(taskID, "live", "unattended"); err != nil {
		return nil, err
	}
	return d.GetTaskV2(taskID)
}

// UnattendedTasks returns tasks the daemon owns.
func (d *DB) UnattendedTasks() ([]Task, error) {
	rows, err := d.q.Query(
		`SELECT ` + taskColumnsV2 + ` FROM tasks WHERE status = 'unattended'`)
	if err != nil {
		return nil, fmt.Errorf("unattended tasks: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := d.scanTaskV2(rows)
		if err != nil {
			return nil, fmt.Errorf("scan unattended: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// IncrementRejectCount bumps the reviewer-rejection counter and returns the
// new value. The daemon blocks the task at the cap rather than burning
// tokens on an unbounded reject/redo loop overnight.
func (d *DB) IncrementRejectCount(taskID string) (int, error) {
	if _, err := d.q.Exec(
		"UPDATE tasks SET reject_count = reject_count + 1 WHERE id = ?", taskID); err != nil {
		return 0, fmt.Errorf("increment reject count: %w", err)
	}
	var n int
	if err := d.q.QueryRow(
		"SELECT reject_count FROM tasks WHERE id = ?", taskID).Scan(&n); err != nil {
		return 0, fmt.Errorf("read reject count: %w", err)
	}
	return n, nil
}

// BoardTasks returns everything the board renders: all open v2 work plus
// tasks closed within the last week (shown collapsed).
func (d *DB) BoardTasks() ([]Task, error) {
	rows, err := d.q.Query(
		`SELECT ` + taskColumnsV2 + ` FROM tasks
		 WHERE status IN ('live','unattended','blocked','parked','proposed')
		    OR (status IN ('done','killed')
		        AND updated_at >= datetime('now','-7 days')
		        AND thought != '')
		 ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("board query: %w", err)
	}
	defer rows.Close()
	var out []Task
	for rows.Next() {
		t, err := d.scanTaskV2(rows)
		if err != nil {
			return nil, fmt.Errorf("scan board row: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// ClosedTask is the slice of a closed row the dedup scorer needs.
type ClosedTask struct {
	ID         string
	Text       string // thought, falling back to title for pre-v2 rows
	Status     string
	KillReason string
	Acceptance string
	UpdatedAt  string
}

// ClosedTasks returns done, killed, and parked tasks for dedup retrieval.
func (d *DB) ClosedTasks() ([]ClosedTask, error) {
	rows, err := d.q.Query(
		`SELECT id,
		        CASE WHEN thought != '' THEN thought ELSE title END,
		        status, COALESCE(kill_reason, ''), COALESCE(acceptance, ''), updated_at
		 FROM tasks WHERE status IN ('done','killed','parked')`)
	if err != nil {
		return nil, fmt.Errorf("closed tasks: %w", err)
	}
	defer rows.Close()
	var out []ClosedTask
	for rows.Next() {
		var c ClosedTask
		if err := rows.Scan(&c.ID, &c.Text, &c.Status, &c.KillReason, &c.Acceptance, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan closed task: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SnapshotJSON renders the board state as JSON — the only view of the ledger
// the model ever sees (PRD I2: no pane text, ever).
func (d *DB) SnapshotJSON() ([]byte, error) {
	tasks, err := d.BoardTasks()
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(tasks, "", "  ")
}

// LastSeen returns when the human last read the brief; zero time if never.
func (d *DB) LastSeen() (time.Time, error) {
	var v string
	err := d.q.QueryRow("SELECT value FROM meta WHERE key = 'last_seen'").Scan(&v)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("last seen: %w", err)
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, nil
	}
	return t, nil
}

// MarkSeen records that the human is caught up as of t.
func (d *DB) MarkSeen(t time.Time) error {
	_, err := d.q.Exec(
		`INSERT INTO meta (key, value) VALUES ('last_seen', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		t.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("mark seen: %w", err)
	}
	return nil
}

// newTaskID generates a unique 4-char hex ID with collision checking.
func (d *DB) newTaskID() (string, error) {
	for i := 0; i < 100; i++ {
		candidate := id.Generate()
		var exists int
		if err := d.q.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", candidate).Scan(&exists); err != nil {
			return "", fmt.Errorf("check id collision: %w", err)
		}
		if exists == 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("failed to generate unique task ID after 100 attempts")
}
