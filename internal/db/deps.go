package db

import (
	"database/sql"
	"fmt"
)

// AddDep creates a dependency where blockerID blocks blockedID.
// Returns an error if either task doesn't exist or the dep would create a cycle.
func (d *DB) AddDep(blockerID, blockedID string) error {
	// Verify both tasks exist.
	for _, tid := range []string{blockerID, blockedID} {
		var count int
		if err := d.q.QueryRow("SELECT COUNT(*) FROM tasks WHERE id = ?", tid).Scan(&count); err != nil {
			return fmt.Errorf("check task %q: %w", tid, err)
		}
		if count == 0 {
			return fmt.Errorf("task %q not found", tid)
		}
	}

	if err := d.checkCycle(blockerID, blockedID); err != nil {
		return err
	}

	_, err := d.q.Exec(
		"INSERT OR IGNORE INTO deps (blocker_id, blocked_id) VALUES (?, ?)",
		blockerID, blockedID,
	)
	if err != nil {
		return fmt.Errorf("insert dep: %w", err)
	}
	return nil
}

// GetBlockers returns the tasks that block the given task.
func (d *DB) GetBlockers(taskID string) ([]Task, error) {
	rows, err := d.q.Query(
		`SELECT t.id, t.title, t.description, t.status, t.block_reason, t.assignee, t.parent_id, t.created_at, t.updated_at
		 FROM tasks t
		 JOIN deps d ON d.blocker_id = t.id
		 WHERE d.blocked_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("get blockers: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// RemoveDep removes a dependency. Returns an error if the dependency does not exist.
func (d *DB) RemoveDep(blockerID, blockedID string) error {
	res, err := d.q.Exec("DELETE FROM deps WHERE blocker_id = ? AND blocked_id = ?", blockerID, blockedID)
	if err != nil {
		return fmt.Errorf("delete dep: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("dependency not found")
	}
	return nil
}

// GetBlocking returns the tasks that this task blocks.
func (d *DB) GetBlocking(taskID string) ([]Task, error) {
	rows, err := d.q.Query(
		`SELECT t.id, t.title, t.description, t.status, t.block_reason, t.assignee, t.parent_id, t.created_at, t.updated_at
		 FROM tasks t
		 JOIN deps d ON d.blocked_id = t.id
		 WHERE d.blocker_id = ?`, taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("get blocking: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// checkCycle returns an error if adding blockerID->blockedID would create a cycle.
func (d *DB) checkCycle(blockerID, blockedID string) error {
	if blockerID == blockedID {
		return fmt.Errorf("task cannot block itself")
	}
	visited := make(map[string]bool)
	return d.dfs(blockerID, blockedID, visited)
}

// dfs walks downstream from current (following blocked_id edges), looking for target.
// If target is reachable from current, adding target->current would create a cycle.
func (d *DB) dfs(target, current string, visited map[string]bool) error {
	if visited[current] {
		return nil
	}
	visited[current] = true

	rows, err := d.q.Query("SELECT blocked_id FROM deps WHERE blocker_id = ?", current)
	if err != nil {
		return fmt.Errorf("dfs query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var blockedID string
		if err := rows.Scan(&blockedID); err != nil {
			return fmt.Errorf("dfs scan: %w", err)
		}
		if blockedID == target {
			return fmt.Errorf("dependency would create a cycle")
		}
		if err := d.dfs(target, blockedID, visited); err != nil {
			return err
		}
	}
	return rows.Err()
}

// scanTasks scans all rows into a slice of Task.
func scanTasks(rows *sql.Rows) ([]Task, error) {
	var tasks []Task
	for rows.Next() {
		var t Task
		if err := rows.Scan(&t.ID, &t.Title, &t.Description, &t.Status, &t.BlockReason, &t.Assignee, &t.ParentID, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows error: %w", err)
	}
	return tasks, nil
}
