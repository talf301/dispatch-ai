package db

import (
	"database/sql"
	"fmt"
	"time"
)

// Task represents a row in the tasks table.
type Task struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	BlockReason *string `json:"block_reason"`
	Assignee    *string `json:"assignee"`
	ParentID    *string `json:"parent_id"`
	Repo        *string `json:"repo"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`

	// v2 (capture-first) fields. Populated by the v2 queries in tasks_v2.go;
	// the legacy queries above leave them zero.
	Thought      string  `json:"thought,omitempty"`
	Label        *string `json:"label,omitempty"`
	Mode         *string `json:"mode,omitempty"` // worktree | in_place
	Workdir      *string `json:"workdir,omitempty"`
	HerdrWs      *string `json:"herdr_ws,omitempty"`
	HerdrTab     *string `json:"herdr_tab,omitempty"`
	HerdrPane    *string `json:"herdr_pane,omitempty"`
	KillReason   *string `json:"kill_reason,omitempty"`
	LastActivity *string `json:"last_activity,omitempty"`

	// M5 (walk-away) fields.
	AcceptanceKind *string `json:"acceptance_kind,omitempty"` // report | ratchet
	Acceptance     *string `json:"acceptance,omitempty"`
	RejectCount    int     `json:"reject_count,omitempty"`
}

// AddTask creates an open task with a unique 4-char hex ID.
// If parentID is non-empty, verifies the parent exists.
// If afterID is non-empty, creates a dependency (afterID blocks the new task).
// repo is an optional repository path associated with the task.
func (d *DB) AddTask(title, description, parentID, afterID string, repo *string) (*Task, error) {
	return d.AddTaskWithStatus(title, description, parentID, afterID, repo, "open")
}

// AddTaskWithStatus is AddTask with an explicit initial status. The
// agent-facing `dt add` passes "proposed": agent-discovered work never
// auto-dispatches until a human approves it with `dt reopen`.
func (d *DB) AddTaskWithStatus(title, description, parentID, afterID string, repo *string, status string) (*Task, error) {
	taskID, err := d.newTaskID()
	if err != nil {
		return nil, err
	}

	// Verify parent exists if set.
	var parentPtr *string
	if parentID != "" {
		var count int
		err := d.q.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", parentID).Scan(&count)
		if err != nil {
			return nil, fmt.Errorf("check parent: %w", err)
		}
		if count == 0 {
			return nil, fmt.Errorf("parent task %q not found", parentID)
		}
		parentPtr = &parentID
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	_, err = d.q.Exec(
		`INSERT INTO tasks (id, title, description, status, parent_id, repo, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		taskID, title, description, status, parentPtr, repo, now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}

	// Add dependency if afterID is set.
	if afterID != "" {
		if err := d.AddDep(afterID, taskID); err != nil {
			return nil, fmt.Errorf("add dep: %w", err)
		}
	}

	return d.GetTask(taskID)
}

// GetTask retrieves a task by ID.
func (d *DB) GetTask(id string) (*Task, error) {
	t := &Task{}
	err := d.q.QueryRow(
		`SELECT id, title, description, status, block_reason, assignee, parent_id, repo, created_at, updated_at
		 FROM tasks WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.BlockReason, &t.Assignee, &t.ParentID, &t.Repo, &t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("task %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("get task: %w", err)
	}
	return t, nil
}

// addSystemNote records a status-change note authored by "system".
func (d *DB) addSystemNote(taskID, oldStatus, newStatus string) error {
	author := "system"
	content := fmt.Sprintf("Status changed: %s → %s", oldStatus, newStatus)
	_, err := d.AddNote(taskID, content, &author)
	return err
}

// ClaimTask assigns a task and sets its status to active.
func (d *DB) ClaimTask(id, assignee string) (*Task, error) {
	task, err := d.GetTask(id)
	if err != nil {
		return nil, err
	}
	oldStatus := task.Status

	// Conditional update: the claim must be decided by the database, not by a
	// read-then-write in the caller — two processes both see a NULL assignee.
	res, err := d.q.Exec(
		"UPDATE tasks SET status = 'active', assignee = ? WHERE id = ? AND assignee IS NULL",
		assignee, id,
	)
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
	}
	if n == 0 {
		holder := "another session"
		if cur, err := d.GetTask(id); err == nil && cur.Assignee != nil {
			holder = *cur.Assignee
		}
		return nil, fmt.Errorf("task %s is already claimed by %s", id, holder)
	}

	if err := d.addSystemNote(id, oldStatus, "active"); err != nil {
		return nil, err
	}
	return d.GetTask(id)
}

// ReleaseTask removes the assignee and sets status to open.
func (d *DB) ReleaseTask(id string) (*Task, error) {
	task, err := d.GetTask(id)
	if err != nil {
		return nil, err
	}

	oldStatus := task.Status
	_, err = d.q.Exec("UPDATE tasks SET status = 'open', assignee = NULL WHERE id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("release task: %w", err)
	}

	if err := d.addSystemNote(id, oldStatus, "open"); err != nil {
		return nil, err
	}
	return d.GetTask(id)
}

// AutoComplete is returned by DoneTask when a parent task was automatically
// completed because all its children are now done.
type AutoComplete struct {
	ParentID string
	Repo     *string
}

// DoneTask marks a task as done and clears the assignee.
// Returns the completed task and every ancestor that auto-completed as a
// result — the immediate parent first, then its parent, and so on.
func (d *DB) DoneTask(id string) (*Task, []AutoComplete, error) {
	_, acs, err := d.doneTask(id)
	if err != nil {
		return nil, nil, err
	}
	t, err := d.GetTask(id)
	if err != nil {
		return nil, nil, err
	}
	return t, acs, nil
}

// doneTask performs the transition and recurses into auto-completing ancestors.
// transitioned is false when the task was already done: the UPDATE is
// conditional, so when two children finish at once and both observe zero
// remaining not-done siblings, only the one that actually moved the parent
// reports its AutoComplete.
func (d *DB) doneTask(id string) (transitioned bool, acs []AutoComplete, err error) {
	task, err := d.GetTask(id)
	if err != nil {
		return false, nil, err
	}

	res, err := d.q.Exec("UPDATE tasks SET status = 'done', assignee = NULL WHERE id = ? AND status != 'done'", id)
	if err != nil {
		return false, nil, fmt.Errorf("done task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, nil, fmt.Errorf("done task: %w", err)
	}
	if n == 0 {
		return false, nil, nil // already done
	}

	if err := d.addSystemNote(id, task.Status, "done"); err != nil {
		return true, nil, err
	}

	if task.ParentID == nil {
		return true, nil, nil
	}

	// Auto-complete parent if all children are done.
	// Note: the count query runs after the UPDATE above, so this task's status
	// is already 'done' in the DB and correctly excluded from the count.
	var notDone int
	if err := d.q.QueryRow(
		`SELECT COUNT(*) FROM tasks WHERE parent_id = ? AND status != 'done'`,
		*task.ParentID,
	).Scan(&notDone); err != nil || notDone > 0 {
		return true, nil, nil
	}

	// Fetch parent to get its Repo before auto-completing.
	parent, err := d.GetTask(*task.ParentID)
	if err != nil {
		return true, nil, fmt.Errorf("auto-complete parent %s: fetch: %w", *task.ParentID, err)
	}
	parentDone, ancestors, err := d.doneTask(parent.ID)
	if err != nil {
		return true, nil, fmt.Errorf("auto-complete parent %s: %w", parent.ID, err)
	}
	if parentDone {
		acs = append([]AutoComplete{{ParentID: parent.ID, Repo: parent.Repo}}, ancestors...)
	}
	return true, acs, nil
}

// BlockTask marks a task as blocked with a reason and clears the assignee.
func (d *DB) BlockTask(id, reason string) (*Task, error) {
	task, err := d.GetTask(id)
	if err != nil {
		return nil, err
	}

	oldStatus := task.Status
	_, err = d.q.Exec("UPDATE tasks SET status = 'blocked', block_reason = ?, assignee = NULL WHERE id = ?", reason, id)
	if err != nil {
		return nil, fmt.Errorf("block task: %w", err)
	}

	if err := d.addSystemNote(id, oldStatus, "blocked"); err != nil {
		return nil, err
	}
	return d.GetTask(id)
}

// ReopenTask sets a task back to open, clearing block_reason and assignee.
func (d *DB) ReopenTask(id string) (*Task, error) {
	task, err := d.GetTask(id)
	if err != nil {
		return nil, err
	}

	oldStatus := task.Status
	_, err = d.q.Exec("UPDATE tasks SET status = 'open', block_reason = NULL, assignee = NULL WHERE id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("reopen task: %w", err)
	}

	if err := d.addSystemNote(id, oldStatus, "open"); err != nil {
		return nil, err
	}
	return d.GetTask(id)
}

// GetChildren returns tasks whose parent_id matches the given ID, ordered by created_at ASC.
func (d *DB) GetChildren(parentID string) ([]Task, error) {
	rows, err := d.q.Query(
		`SELECT id, title, description, status, block_reason, assignee, parent_id, repo, created_at, updated_at
		 FROM tasks WHERE parent_id = ? ORDER BY created_at ASC`, parentID,
	)
	if err != nil {
		return nil, fmt.Errorf("get children: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ReadyTasks returns open, unassigned tasks whose blockers are all done,
// ordered by the number of tasks they unblock (desc), then created_at (asc).
func (d *DB) ReadyTasks() ([]Task, error) {
	rows, err := d.q.Query(`
		SELECT t.id, t.title, t.description, t.status, t.block_reason,
		       t.assignee, t.parent_id, t.repo, t.created_at, t.updated_at
		FROM tasks t
		WHERE t.status = 'open'
		  AND t.assignee IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM deps d
		    JOIN tasks blocker ON d.blocker_id = blocker.id
		    WHERE d.blocked_id = t.id
		    AND blocker.status != 'done'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM tasks child WHERE child.parent_id = t.id
		  )
		ORDER BY (
		    SELECT COUNT(*) FROM deps d2
		    WHERE d2.blocker_id = t.id
		    AND EXISTS (SELECT 1 FROM tasks t2 WHERE t2.id = d2.blocked_id AND t2.status != 'done')
		  ) DESC,
		  t.created_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("ready tasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ListTasks returns tasks filtered by status. If status is set, only that status
// is returned. If all is false and status is empty, done tasks are excluded.
func (d *DB) ListTasks(status string, all bool) ([]Task, error) {
	var query string
	var args []any

	if status != "" {
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, repo, created_at, updated_at
		         FROM tasks WHERE status = ? ORDER BY created_at ASC`
		args = append(args, status)
	} else if !all {
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, repo, created_at, updated_at
		         FROM tasks WHERE status != 'done' ORDER BY created_at ASC`
	} else {
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, repo, created_at, updated_at
		         FROM tasks ORDER BY created_at ASC`
	}

	rows, err := d.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// EditTask updates a task's title, description, and/or repo. Fields that are nil are left unchanged.
func (d *DB) EditTask(id string, title, description, repo *string) (*Task, error) {
	if title == nil && description == nil && repo == nil {
		return d.GetTask(id)
	}

	// Verify task exists.
	if _, err := d.GetTask(id); err != nil {
		return nil, err
	}

	if title != nil {
		if _, err := d.q.Exec("UPDATE tasks SET title = ? WHERE id = ?", *title, id); err != nil {
			return nil, fmt.Errorf("update title: %w", err)
		}
	}
	if description != nil {
		if _, err := d.q.Exec("UPDATE tasks SET description = ? WHERE id = ?", *description, id); err != nil {
			return nil, fmt.Errorf("update description: %w", err)
		}
	}
	if repo != nil {
		if _, err := d.q.Exec("UPDATE tasks SET repo = ? WHERE id = ?", *repo, id); err != nil {
			return nil, fmt.Errorf("update repo: %w", err)
		}
	}

	return d.GetTask(id)
}

// PendingPRParents returns done parent tasks where all children are also done
// and the parent has a repo set. These are candidates for automatic PR creation.
func (d *DB) PendingPRParents() ([]Task, error) {
	rows, err := d.q.Query(`
		SELECT t.id, t.title, t.description, t.status, t.block_reason,
		       t.assignee, t.parent_id, t.repo, t.created_at, t.updated_at
		FROM tasks t
		WHERE t.status = 'done'
		  AND EXISTS (
		    SELECT 1 FROM tasks child WHERE child.parent_id = t.id
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM tasks child
		    WHERE child.parent_id = t.id AND child.status != 'done'
		  )
		  AND NOT EXISTS (
		    SELECT 1 FROM meta WHERE key = 'pr.handled.' || t.id
		  )
		  AND t.repo IS NOT NULL
		ORDER BY t.updated_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("pending PR parents: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// MarkPRHandled removes a completed parent from the daemon's PR retry queue.
func (d *DB) MarkPRHandled(taskID string) error {
	_, err := d.q.Exec(`INSERT INTO meta (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, "pr.handled."+taskID, "1")
	if err != nil {
		return fmt.Errorf("mark PR handled: %w", err)
	}
	return nil
}
