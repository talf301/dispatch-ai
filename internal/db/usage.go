package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Attempt is one provider process started for a task. Usage is provider facts,
// not a normalized token score or a cost estimate.
type Attempt struct {
	ID                int64           `json:"id"`
	AttemptKey        string          `json:"attempt_key"`
	TaskID            string          `json:"task_id"`
	Role              string          `json:"role"`
	Provider          string          `json:"provider"`
	Model             *string         `json:"model,omitempty"`
	StartedAt         string          `json:"started_at"`
	EndedAt           *string         `json:"ended_at,omitempty"`
	ExitStatus        *int            `json:"exit_status,omitempty"`
	InputTokens       *int64          `json:"input_tokens,omitempty"`
	CachedInputTokens *int64          `json:"cached_input_tokens,omitempty"`
	OutputTokens      *int64          `json:"output_tokens,omitempty"`
	ReasoningTokens   *int64          `json:"reasoning_tokens,omitempty"`
	TurnCount         *int            `json:"turn_count,omitempty"`
	ToolOutputBytes   *int64          `json:"tool_output_bytes,omitempty"`
	WaitOnlyCount     *int            `json:"wait_only_count,omitempty"`
	RawUsage          json.RawMessage `json:"raw_usage,omitempty"`
}

type UsageTotals struct {
	Attempts          int   `json:"attempts"`
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
	Turns             int   `json:"turns"`
	ToolOutputBytes   int64 `json:"tool_output_bytes"`
}

type UsageReport struct {
	TaskID   string      `json:"task_id"`
	Attempts []Attempt   `json:"attempts"`
	Totals   UsageTotals `json:"totals"`
}

func (d *DB) StartAttempt(key, taskID, role, provider string, model *string) error {
	_, err := d.q.Exec(`INSERT OR IGNORE INTO task_attempts
		(attempt_key, task_id, role, provider, model, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`, key, taskID, role, provider, model,
		time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (d *DB) FinishAttempt(key string, a Attempt) error {
	var raw any
	if len(a.RawUsage) > 0 && string(a.RawUsage) != "null" {
		raw = string(a.RawUsage)
	}
	_, err := d.q.Exec(`UPDATE task_attempts SET ended_at=?, exit_status=?, model=COALESCE(?, model),
		input_tokens=?, cached_input_tokens=?, output_tokens=?, reasoning_tokens=?, turn_count=?,
		tool_output_bytes=?, wait_only_count=?, raw_usage=? WHERE attempt_key=? AND ended_at IS NULL`,
		now(), a.ExitStatus, a.Model, a.InputTokens, a.CachedInputTokens, a.OutputTokens,
		a.ReasoningTokens, a.TurnCount, a.ToolOutputBytes, a.WaitOnlyCount, raw, key)
	return err
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func (d *DB) Usage(taskID string) (*UsageReport, error) {
	query := `SELECT id, attempt_key, task_id, role, provider, model, started_at, ended_at,
		exit_status, input_tokens, cached_input_tokens, output_tokens, reasoning_tokens, turn_count,
		tool_output_bytes, wait_only_count, raw_usage FROM task_attempts`
	args := []any{}
	if taskID != "" {
		query += " WHERE task_id = ?"
		args = append(args, taskID)
	}
	query += " ORDER BY id"
	rows, err := d.q.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query attempts: %w", err)
	}
	defer rows.Close()
	r := &UsageReport{TaskID: taskID, Attempts: []Attempt{}}
	for rows.Next() {
		var a Attempt
		var model, ended, raw sql.NullString
		var exit, input, cached, output, reasoning, turns, bytes, waits sql.NullInt64
		if err := rows.Scan(&a.ID, &a.AttemptKey, &a.TaskID, &a.Role, &a.Provider, &model, &a.StartedAt, &ended,
			&exit, &input, &cached, &output, &reasoning, &turns, &bytes, &waits, &raw); err != nil {
			return nil, err
		}
		if model.Valid {
			a.Model = &model.String
		}
		if ended.Valid {
			a.EndedAt = &ended.String
		}
		if exit.Valid {
			v := int(exit.Int64)
			a.ExitStatus = &v
		}
		setInt64 := func(v sql.NullInt64) *int64 {
			if !v.Valid {
				return nil
			}
			x := v.Int64
			return &x
		}
		a.InputTokens = setInt64(input)
		a.CachedInputTokens = setInt64(cached)
		a.OutputTokens = setInt64(output)
		a.ReasoningTokens = setInt64(reasoning)
		a.ToolOutputBytes = setInt64(bytes)
		setInt := func(v sql.NullInt64) *int {
			if !v.Valid {
				return nil
			}
			x := int(v.Int64)
			return &x
		}
		a.TurnCount = setInt(turns)
		a.WaitOnlyCount = setInt(waits)
		if raw.Valid {
			a.RawUsage = json.RawMessage(raw.String)
		}
		r.Attempts = append(r.Attempts, a)
		r.Totals.Attempts++
		r.Totals.InputTokens += value64(a.InputTokens)
		r.Totals.CachedInputTokens += value64(a.CachedInputTokens)
		r.Totals.OutputTokens += value64(a.OutputTokens)
		r.Totals.ReasoningTokens += value64(a.ReasoningTokens)
		r.Totals.Turns += valueInt(a.TurnCount)
		r.Totals.ToolOutputBytes += value64(a.ToolOutputBytes)
	}
	return r, rows.Err()
}

func value64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
