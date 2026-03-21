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

// checkCycle returns an error if adding blockerID->blockedID would create a cycle.
func (d *DB) checkCycle(blockerID, blockedID string) error {
	if blockerID == blockedID {
		return fmt.Errorf("task cannot block itself")
	}
	visited := make(map[string]bool)
	return d.dfs(blockerID, blockedID, visited)
}

// dfs walks blocker_id edges starting from current, looking for target.
// If target is reachable from current, adding target->current would create a cycle.
func (d *DB) dfs(target, current string, visited map[string]bool) error {
	if visited[current] {
		return nil
	}
	visited[current] = true

	rows, err := d.q.Query("SELECT blocker_id FROM deps WHERE blocked_id = ?", current)
	if err != nil {
		return fmt.Errorf("dfs query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var blockerID string
		if err := rows.Scan(&blockerID); err != nil {
			return fmt.Errorf("dfs scan: %w", err)
		}
		if blockerID == target {
			return fmt.Errorf("dependency would create a cycle")
		}
		if err := d.dfs(target, blockerID, visited); err != nil {
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
