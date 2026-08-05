package db

import (
	"fmt"
	"time"
)

// SecondmateInvestigation is the durable audit row for one blocked-task scan.
type SecondmateInvestigation struct {
	ID             int64  `json:"id"`
	TaskID         string `json:"task_id"`
	BlockReason    string `json:"block_reason"`
	Classification string `json:"classification"`
	Action         string `json:"action"`
	Outcome        string `json:"outcome"`
	RetryKey       string `json:"retry_key"`
	RetryCount     int    `json:"retry_count"`
	CreatedAt      string `json:"created_at"`
}

func (d *DB) AddSecondmateInvestigation(i SecondmateInvestigation) error {
	_, err := d.q.Exec(`INSERT INTO secondmate_investigations
		(task_id, block_reason, classification, action, outcome, retry_key, retry_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, i.TaskID, i.BlockReason, i.Classification,
		i.Action, i.Outcome, i.RetryKey, i.RetryCount, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record secondmate investigation: %w", err)
	}
	return nil
}

func (d *DB) SecondmateInvestigations(taskID string) ([]SecondmateInvestigation, error) {
	rows, err := d.q.Query(`SELECT id, task_id, block_reason, classification, action,
		outcome, retry_key, retry_count, created_at FROM secondmate_investigations
		WHERE task_id = ? ORDER BY id`, taskID)
	if err != nil {
		return nil, fmt.Errorf("query secondmate investigations: %w", err)
	}
	defer rows.Close()
	var out []SecondmateInvestigation
	for rows.Next() {
		var i SecondmateInvestigation
		if err := rows.Scan(&i.ID, &i.TaskID, &i.BlockReason, &i.Classification, &i.Action,
			&i.Outcome, &i.RetryKey, &i.RetryCount, &i.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan secondmate investigation: %w", err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}
