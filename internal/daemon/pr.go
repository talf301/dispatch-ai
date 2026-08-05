package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

// createPR pushes headBranch and creates a GitHub PR for a completed task -
// a plan branch (dispatch/plan-<id>) for a finished multi-child plan, or a
// task's own branch (dispatch/<id>) for a standalone task acting as a plan
// of one. allowZeroDiffSuccess is only true for plan branches: a standalone
// branch with no diff must still surface the gh error before its worktree is
// deleted. Treating "a PR already exists for this head" as success (not an
// error) makes this safe to call more than once for the same task, which
// matters for both the retry queries below and daemon-restart recovery.
func (d *Daemon) createPR(repoPath, headBranch string, task db.Task, allowZeroDiffSuccess bool) error {
	mode := config.DefaultDeliveryMode
	if repo, ok := d.repos[repoPath]; ok && repo.DeliveryMode != "" {
		mode = repo.DeliveryMode
	}
	if mode == config.DeliveryModeLocalOnly {
		return d.mergeLocal(repoPath, headBranch, task)
	}

	baseBranch, err := d.baseBranchFor(&task)
	if err != nil {
		return fmt.Errorf("resolve PR base: %w", err)
	}

	baseName := strings.TrimPrefix(baseBranch, "origin/")
	if err := FetchOriginBranch(repoPath, baseName); err != nil {
		return fmt.Errorf("refresh default branch: %w", err)
	}
	// Compare against the remote base, which is what GitHub uses for the PR
	// diff. Only trust a successful git result; errors must continue to the
	// normal path.
	remoteBase := "origin/" + baseName
	if count, err := branchCommitCount(repoPath, remoteBase, headBranch); err == nil {
		if count == 0 && allowZeroDiffSuccess {
			note := fmt.Sprintf("PR skipped: %s has no commits relative to %s; changes were already merged elsewhere.", headBranch, remoteBase)
			author := "daemon"
			if _, err := d.db.AddNote(task.ID, note, &author); err != nil {
				return fmt.Errorf("record zero-diff PR note: %w", err)
			}
			if err := d.db.MarkPRHandled(task.ID); err != nil {
				return fmt.Errorf("record zero-diff PR: %w", err)
			}
			d.logger.Printf("PR for %s (%s) skipped: no commits between %s and %s; already merged elsewhere", task.ID, headBranch, remoteBase, headBranch)
			return nil
		}
	} else {
		d.logger.Printf("could not count commits between %s and %s: %v; attempting normal PR creation", remoteBase, headBranch, err)
	}
	// Push headBranch to origin.
	pushCmd := exec.Command("git", "push", "origin", headBranch)
	pushCmd.Dir = repoPath
	if out, err := pushCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git push: %w\n%s", err, out)
	}

	// Fetch notes on the task for the PR body.
	notes, err := d.db.GetNotes(task.ID)
	if err != nil {
		return fmt.Errorf("get notes: %w", err)
	}

	body := formatPRBody(notes)

	// Create the PR via gh CLI.
	ghCmd := exec.Command("gh", "pr", "create",
		"--head", headBranch,
		// GitHub wants the branch name, not the local remote-tracking ref.
		"--base", strings.TrimPrefix(baseBranch, "origin/"),
		"--title", task.Title,
		"--body", body,
	)
	ghCmd.Dir = repoPath
	if out, err := ghCmd.CombinedOutput(); err != nil {
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			if err := d.db.MarkPRHandled(task.ID); err != nil {
				return fmt.Errorf("record existing PR: %w", err)
			}
			d.logger.Printf("PR for %s (%s) already exists, treating as success", task.ID, headBranch)
			return nil
		}
		return fmt.Errorf("gh pr create: %w\n%s", err, out)
	}
	if err := d.db.MarkPRHandled(task.ID); err != nil {
		return fmt.Errorf("record created PR: %w", err)
	}

	d.logger.Printf("created PR for %s (%s)", task.ID, task.Title)
	return nil
}

func (d *Daemon) mergeLocal(repoPath, headBranch string, task db.Task) error {
	baseBranch, err := d.baseBranchFor(&task)
	if err != nil {
		return fmt.Errorf("resolve local merge base: %w", err)
	}
	if err := MergeBranch(repoPath, headBranch, baseBranch); err != nil {
		return fmt.Errorf("local merge %s into %s: %w", headBranch, baseBranch, err)
	}
	if err := d.db.MarkPRHandled(task.ID); err != nil {
		return fmt.Errorf("record local merge: %w", err)
	}
	d.logger.Printf("locally merged %s into %s", task.ID, baseBranch)
	return nil
}

func branchCommitCount(repoPath, baseBranch, headBranch string) (int, error) {
	cmd := exec.Command("git", "rev-list", "--count", baseBranch+".."+headBranch)
	cmd.Dir = repoPath
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("count commits between %s and %s: %w", baseBranch, headBranch, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return 0, fmt.Errorf("parse commit count: %w", err)
	}
	return n, nil
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

	repoPath, err := d.taskRepoPath(parent)
	if err != nil {
		d.logger.Printf("trigger-pr: %v", err)
		return
	}
	if _, ok := d.repos[repoPath]; !ok {
		d.logger.Printf("trigger-pr: parent %s references unknown repo %q", ac.ParentID, repoPath)
		return
	}

	planBranch := fmt.Sprintf("dispatch/plan-%s", ac.ParentID)
	if err := d.createPR(repoPath, planBranch, *parent, true); err != nil {
		reason := fmt.Sprintf("pr: %v", err)
		if len(reason) > 4000 {
			reason = reason[:4000]
		}
		d.logger.Printf("trigger-pr: PR creation failed for %s: %v", ac.ParentID, err)
		if _, err := d.db.BlockTaskWithKind(ac.ParentID, reason, db.BlockKindPRCreateFailed); err != nil {
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
		repoPath, err := d.taskRepoPath(&parent)
		if err != nil {
			d.logger.Printf("pending-prs: %v, skipping", err)
			continue
		}
		if _, ok := d.repos[repoPath]; !ok {
			d.logger.Printf("pending-prs: parent %s references unknown repo %q, skipping", parent.ID, repoPath)
			continue
		}

		planBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
		if err := d.createPR(repoPath, planBranch, parent, true); err != nil {
			reason := fmt.Sprintf("pr: %v", err)
			if len(reason) > 4000 {
				reason = reason[:4000]
			}
			d.logger.Printf("pending-prs: PR creation failed for %s: %v", parent.ID, err)
			if _, err := d.db.BlockTaskWithKind(parent.ID, reason, db.BlockKindPRCreateFailed); err != nil {
				d.logger.Printf("pending-prs: block parent %s: %v", parent.ID, err)
			}
		}
	}
}
