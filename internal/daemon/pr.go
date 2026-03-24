package daemon

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
)

// createPR pushes the plan branch and creates a GitHub PR for a completed parent task.
func (d *Daemon) createPR(repoPath string, parentTask db.Task) error {
	planBranch := fmt.Sprintf("dispatch/plan-%s", parentTask.ID)

	// Push the plan branch to origin.
	pushCmd := exec.Command("git", "push", "origin", planBranch)
	pushCmd.Dir = repoPath
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, out)
	}

	// Detect the default branch for the PR base.
	baseBranch := d.baseBranch
	if baseBranch == "" {
		var err error
		baseBranch, err = DetectDefaultBranch(repoPath)
		if err != nil {
			return fmt.Errorf("detect default branch: %w", err)
		}
	}

	// Fetch notes on the parent task for the PR body.
	notes, err := d.db.GetNotes(parentTask.ID)
	if err != nil {
		return fmt.Errorf("get notes: %w", err)
	}

	body := formatPRBody(notes)

	// Create the PR via gh CLI.
	ghCmd := exec.Command("gh", "pr", "create",
		"--head", planBranch,
		"--base", baseBranch,
		"--title", parentTask.Title,
		"--body", body,
	)
	ghCmd.Dir = repoPath
	if out, err := ghCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr create: %w\n%s", err, out)
	}

	d.logger.Printf("created PR for plan %s (%s)", parentTask.ID, parentTask.Title)
	return nil
}

// formatPRBody assembles the PR body from parent task notes.
func formatPRBody(notes []db.Note) string {
	var b strings.Builder
	b.WriteString("## Summary\n\n")

	if len(notes) == 0 {
		b.WriteString("_No worker notes recorded._\n")
	} else {
		for _, n := range notes {
			// Skip system notes (status changes).
			if n.Author != nil && *n.Author == "system" {
				continue
			}
			b.WriteString("- ")
			b.WriteString(n.Content)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n---\nCreated by [dispatch](https://github.com/dispatch-ai/dispatch)\n")
	return b.String()
}

// triggerPR is called from handleReviewApproval when DoneTask returns a non-nil
// AutoComplete, indicating a parent plan just completed.
func (d *Daemon) triggerPR(ac *db.AutoComplete) {
	parent, err := d.db.GetTask(ac.ParentID)
	if err != nil {
		d.logger.Printf("trigger-pr: get parent %s: %v", ac.ParentID, err)
		return
	}

	repoPath := d.taskRepoPath(parent)
	if _, ok := d.repos[repoPath]; !ok {
		d.logger.Printf("trigger-pr: parent %s references unknown repo %q", ac.ParentID, repoPath)
		return
	}

	if err := d.createPR(repoPath, *parent); err != nil {
		reason := fmt.Sprintf("pr: %v", err)
		if len(reason) > 4000 {
			reason = reason[:4000]
		}
		d.logger.Printf("trigger-pr: PR creation failed for %s: %v", ac.ParentID, err)
		if _, err := d.db.BlockTask(ac.ParentID, reason); err != nil {
			d.logger.Printf("trigger-pr: block parent %s: %v", ac.ParentID, err)
		}
	}
}

// checkPendingPRs queries for completed parent tasks that need PRs and attempts
// to create them. Called each poll cycle in the Run() loop.
func (d *Daemon) checkPendingPRs() {
	parents, err := d.db.PendingPRParents()
	if err != nil {
		d.logger.Printf("pending-prs: query: %v", err)
		return
	}

	for _, parent := range parents {
		repoPath := d.taskRepoPath(&parent)
		if _, ok := d.repos[repoPath]; !ok {
			d.logger.Printf("pending-prs: parent %s references unknown repo %q, skipping", parent.ID, repoPath)
			continue
		}

		if err := d.createPR(repoPath, parent); err != nil {
			reason := fmt.Sprintf("pr: %v", err)
			if len(reason) > 4000 {
				reason = reason[:4000]
			}
			d.logger.Printf("pending-prs: PR creation failed for %s: %v", parent.ID, err)
			if _, err := d.db.BlockTask(parent.ID, reason); err != nil {
				d.logger.Printf("pending-prs: block parent %s: %v", parent.ID, err)
			}
		}
	}
}
