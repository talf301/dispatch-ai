package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type ReviewFinding struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Detail  string `json:"detail"`
}

type ReviewDigest struct {
	ScannedAt string          `json:"scanned_at"`
	Findings  []ReviewFinding `json:"findings"`
}

func (d *DB) SaveReviewDigest(digest ReviewDigest) error {
	b, err := json.Marshal(digest)
	if err != nil {
		return fmt.Errorf("marshal review digest: %w", err)
	}
	_, err = d.q.Exec(`INSERT INTO meta (key, value) VALUES ('review_digest', ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, string(b))
	if err != nil {
		return fmt.Errorf("save review digest: %w", err)
	}
	return nil
}

func (d *DB) LoadReviewDigest() (*ReviewDigest, error) {
	var value string
	if err := d.q.QueryRow("SELECT value FROM meta WHERE key = 'review_digest'").Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("load review digest: %w", err)
	}
	var digest ReviewDigest
	if err := json.Unmarshal([]byte(value), &digest); err != nil {
		return nil, fmt.Errorf("decode review digest: %w", err)
	}
	return &digest, nil
}
