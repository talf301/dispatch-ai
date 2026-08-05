package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

const staleTaskAfter = 24 * time.Hour

// scanReview is a zero-token fleet health pass. It persists the current
// findings and logs only newly appearing findings.
func (d *Daemon) scanReview(now time.Time) {
	tasks, err := d.db.ReviewTasks()
	if err != nil {
		d.logger.Printf("review: list tasks: %v", err)
		return
	}
	findings := staleFindings(tasks, now)
	findings = append(findings, d.orphanWorktreeFindings(tasks)...)
	findings = append(findings, d.orphanBranchFindings(tasks)...)
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	previous, err := d.db.LoadReviewDigest()
	if err != nil {
		d.logger.Printf("review: load previous digest: %v", err)
	}
	old := make(map[string]bool)
	if previous != nil {
		for _, f := range previous.Findings {
			old[f.ID] = true
		}
	}
	for _, f := range findings {
		if !old[f.ID] {
			d.logger.Printf("review: %s", f.Detail)
		}
	}
	if err := d.db.SaveReviewDigest(db.ReviewDigest{ScannedAt: now.UTC().Format(time.RFC3339), Findings: findings}); err != nil {
		d.logger.Printf("review: save digest: %v", err)
	}
}

func staleFindings(tasks []db.Task, now time.Time) []db.ReviewFinding {
	var out []db.ReviewFinding
	for _, t := range tasks {
		if t.Status != "live" && t.Status != "active" && t.Status != "unattended" && t.Status != "blocked" {
			continue
		}
		stamp := t.UpdatedAt
		if t.LastActivity != nil && *t.LastActivity != "" {
			if activity, err := time.Parse("2006-01-02 15:04:05", *t.LastActivity); err == nil {
				if updated, err := time.Parse("2006-01-02 15:04:05", stamp); err != nil || activity.After(updated) {
					stamp = *t.LastActivity
				}
			}
		}
		at, err := time.Parse("2006-01-02 15:04:05", stamp)
		if err != nil || now.Sub(at) < staleTaskAfter {
			continue
		}
		out = append(out, db.ReviewFinding{ID: "stale_task:" + t.ID, Kind: "stale_task", Subject: t.ID,
			Detail: fmt.Sprintf("task %s has had no activity or status change since %s (%s)", t.ID, stamp, t.Status)})
	}
	return out
}

func (d *Daemon) orphanWorktreeFindings(tasks []db.Task) []db.ReviewFinding {
	entries, err := os.ReadDir(d.worktreeBase)
	if err != nil {
		return nil
	}
	active := make(map[string]bool)
	for _, t := range tasks {
		if t.Status != "done" && t.Status != "killed" {
			active[t.ID] = true
		}
	}
	var out []db.ReviewFinding
	for _, entry := range entries {
		if entry.IsDir() && !active[entry.Name()] {
			out = append(out, db.ReviewFinding{ID: "orphan_worktree:" + entry.Name(), Kind: "orphan_worktree", Subject: entry.Name(), Detail: fmt.Sprintf("worktree %s has no live task row", entry.Name())})
		}
	}
	return out
}

func (d *Daemon) orphanBranchFindings(tasks []db.Task) []db.ReviewFinding {
	known := make(map[string]bool)
	for _, t := range tasks {
		if t.Status != "done" && t.Status != "killed" {
			known[t.ID] = true
		}
	}
	var out []db.ReviewFinding
	for repo := range d.repos {
		branches, err := exec.Command("git", "-C", repo, "for-each-ref", "--format=%(refname:short)", "refs/heads/dispatch/").Output()
		if err != nil {
			continue
		}
		for _, branch := range strings.Fields(string(branches)) {
			name := strings.TrimPrefix(branch, "dispatch/")
			id := strings.TrimPrefix(name, "plan-")
			if !known[id] {
				subject := filepath.Join(repo, branch)
				out = append(out, db.ReviewFinding{ID: "orphan_branch:" + subject, Kind: "orphan_branch", Subject: subject, Detail: fmt.Sprintf("branch %s has no corresponding task", subject)})
			}
		}
	}
	return out
}
