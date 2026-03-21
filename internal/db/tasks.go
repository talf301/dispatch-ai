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
