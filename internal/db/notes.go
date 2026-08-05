package db

import (
	"fmt"
	"sync"
)

// Event is a durable-ledger transition signal. It is a wakeup hint, not a
// second source of truth; consumers must read the task from the ledger.
type Event struct {
	TaskID    string
	OldStatus string
	NewStatus string
}

// Note represents a row in the notes table.
type Note struct {
	ID        int     `json:"id"`
	TaskID    string  `json:"task_id"`
	Content   string  `json:"content"`
	Author    *string `json:"author"`
	CreatedAt string  `json:"created_at"`
}

// AddNote inserts a note for the given task. Returns the created note.
func (d *DB) AddNote(taskID, content string, author *string) (*Note, error) {
	// Verify task exists.
	if _, err := d.GetTask(taskID); err != nil {
		return nil, err
	}

	res, err := d.q.Exec(
		`INSERT INTO notes (task_id, content, author) VALUES (?, ?, ?)`,
		taskID, content, author,
	)
	if err != nil {
		return nil, fmt.Errorf("insert note: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}

	n := &Note{}
	err = d.q.QueryRow(
		`SELECT id, task_id, content, author, created_at FROM notes WHERE id = ?`, id,
	).Scan(&n.ID, &n.TaskID, &n.Content, &n.Author, &n.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("get note: %w", err)
	}
	return n, nil
}

var eventSubscribers struct {
	sync.Mutex
	byDB map[*DB]map[chan Event]struct{}
}

// Subscribe returns an event stream and a cancellation function. Delivery is
// best effort: a slow conversational surface must never block task writes.
func (d *DB) Subscribe() (<-chan Event, func()) {
	eventSubscribers.Lock()
	if eventSubscribers.byDB == nil {
		eventSubscribers.byDB = make(map[*DB]map[chan Event]struct{})
	}
	if eventSubscribers.byDB[d] == nil {
		eventSubscribers.byDB[d] = make(map[chan Event]struct{})
	}
	ch := make(chan Event, 16)
	eventSubscribers.byDB[d][ch] = struct{}{}
	eventSubscribers.Unlock()
	return ch, func() {
		eventSubscribers.Lock()
		delete(eventSubscribers.byDB[d], ch)
		close(ch)
		eventSubscribers.Unlock()
	}
}

func (d *DB) publish(event Event) {
	eventSubscribers.Lock()
	defer eventSubscribers.Unlock()
	for ch := range eventSubscribers.byDB[d] {
		select {
		case ch <- event:
		default:
		}
	}
}

// GetNotes returns all notes for a task, ordered by created_at ascending.
func (d *DB) GetNotes(taskID string) ([]Note, error) {
	rows, err := d.q.Query(
		`SELECT id, task_id, content, author, created_at FROM notes WHERE task_id = ? ORDER BY created_at ASC`,
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("query notes: %w", err)
	}
	defer rows.Close()

	notes := []Note{}
	for rows.Next() {
		var n Note
		if err := rows.Scan(&n.ID, &n.TaskID, &n.Content, &n.Author, &n.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan note: %w", err)
		}
		notes = append(notes, n)
	}
	return notes, rows.Err()
}
