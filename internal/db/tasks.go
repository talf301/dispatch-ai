package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/dispatch-ai/dispatch/internal/id"
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
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// AddTask creates a new task with a unique 4-char hex ID.
// If parentID is non-empty, verifies the parent exists.
// If afterID is non-empty, creates a dependency (afterID blocks the new task).
func (d *DB) AddTask(title, description, parentID, afterID string) (*Task, error) {
	// Generate unique ID with collision check.
	var taskID string
	for i := 0; i < 100; i++ {
		candidate := id.Generate()
		var exists int
		err := d.q.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", candidate).Scan(&exists)
		if err != nil {
			return nil, fmt.Errorf("check id collision: %w", err)
		}
		if exists == 0 {
			taskID = candidate
			break
		}
	}
	if taskID == "" {
		return nil, fmt.Errorf("failed to generate unique task ID after 100 attempts")
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

	_, err := d.q.Exec(
		`INSERT INTO tasks (id, title, description, parent_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		taskID, title, description, parentPtr, now, now,
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
		`SELECT id, title, description, status, block_reason, assignee, parent_id, created_at, updated_at
		 FROM tasks WHERE id = ?`, id,
	).Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.BlockReason, &t.Assignee, &t.ParentID, &t.CreatedAt, &t.UpdatedAt)
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
	if task.Assignee != nil {
		return nil, fmt.Errorf("task %s is already claimed by %s", id, *task.Assignee)
	}

	oldStatus := task.Status
	_, err = d.q.Exec("UPDATE tasks SET status = 'active', assignee = ? WHERE id = ?", assignee, id)
	if err != nil {
		return nil, fmt.Errorf("claim task: %w", err)
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

// DoneTask marks a task as done and clears the assignee.
func (d *DB) DoneTask(id string) (*Task, error) {
	task, err := d.GetTask(id)
	if err != nil {
		return nil, err
	}

	oldStatus := task.Status
	_, err = d.q.Exec("UPDATE tasks SET status = 'done', assignee = NULL WHERE id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("done task: %w", err)
	}

	if err := d.addSystemNote(id, oldStatus, "done"); err != nil {
		return nil, err
	}
	return d.GetTask(id)
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
		`SELECT id, title, description, status, block_reason, assignee, parent_id, created_at, updated_at
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
		       t.assignee, t.parent_id, t.created_at, t.updated_at
		FROM tasks t
		WHERE t.status = 'open'
		  AND t.assignee IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM deps d
		    JOIN tasks blocker ON d.blocker_id = blocker.id
		    WHERE d.blocked_id = t.id
		    AND blocker.status != 'done'
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
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, created_at, updated_at
		         FROM tasks WHERE status = ? ORDER BY created_at ASC`
		args = append(args, status)
	} else if !all {
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, created_at, updated_at
		         FROM tasks WHERE status != 'done' ORDER BY created_at ASC`
	} else {
		query = `SELECT id, title, description, status, block_reason, assignee, parent_id, created_at, updated_at
		         FROM tasks ORDER BY created_at ASC`
	}

	rows, err := d.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// EditTask updates a task's title and/or description. Fields that are nil are left unchanged.
func (d *DB) EditTask(id string, title, description *string) (*Task, error) {
	if title == nil && description == nil {
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

	return d.GetTask(id)
}
