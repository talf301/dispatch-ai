package commands

import (
	"fmt"

	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func warnIfBlockerIsBlocked(database *db.DB, blockerID string) string {
	task, err := database.GetTask(blockerID)
	if err != nil || task.Status != "blocked" {
		return ""
	}
	reason := "unspecified"
	if task.BlockReason != nil && *task.BlockReason != "" {
		reason = *task.BlockReason
	}
	return fmt.Sprintf("warning: task %s is currently blocked (reason: %s). The dependent task will not be schedulable until %s reaches done. If the dependent work is meant to unblock %s, do not create this dependency.", blockerID, reason, blockerID, blockerID)
}

// warnIfOrphanFromLivePlan returns a one-line warning if repo points at a
// live task's worktree and no parent was given. A live task can never be
// --parent - it doesn't go through the daemon's worker/review cycle, so it
// never gets a dispatch/plan-<id> branch for children to accumulate into -
// so a task created this way completes with its own solo PR rather than
// joining a shared plan. Returns "" when there's nothing to warn about.
func warnIfOrphanFromLivePlan(database *db.DB, repo *string, parent string) string {
	if repo == nil || parent != "" {
		return ""
	}
	live, err := database.ListTasks("live", true)
	if err != nil {
		return ""
	}
	for _, t := range live {
		if t.Repo != nil && *t.Repo == *repo {
			return fmt.Sprintf(
				"warning: -r points at live task %s's worktree - it can't be --parent, so this task will get its own PR on completion. If this is part of a batch that should land as one PR, create a plan parent task first and pass --parent <id>.",
				t.ID)
		}
	}
	return ""
}

// NewAddCmd returns the cobra command for adding a task.
func NewAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <title>",
		Short: "Add a new task",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			title := args[0]
			desc, _ := cmd.Flags().GetString("desc")
			parent, _ := cmd.Flags().GetString("parent")
			after, _ := cmd.Flags().GetString("after")
			agentValue, _ := cmd.Flags().GetString("agent")
			agent, err := daemon.ValidateAgent(agentValue)
			if err != nil {
				exitError(cmd, err)
			}
			if after != "" {
				if warning := warnIfBlockerIsBlocked(d, after); warning != "" {
					cmd.PrintErrln(warning)
				}
			}
			baseBranch, _ := cmd.Flags().GetString("base-branch")
			if parent != "" && baseBranch != "" {
				exitError(cmd, fmt.Errorf("--base-branch cannot be used with --parent; children start from the parent plan branch"))
			}

			var repo *string
			if cmd.Flags().Changed("repo") {
				v, _ := cmd.Flags().GetString("repo")
				repo = &v
			}

			if w := warnIfOrphanFromLivePlan(d, repo, parent); w != "" {
				cmd.PrintErrln(w)
			}

			// Agent-facing path: new work is proposed, never auto-dispatched.
			// A human approves it with `dt reopen <id>`.
			task, err := d.AddTaskWithAgent(title, desc, parent, after, repo, "proposed", agent)
			if err != nil {
				exitError(cmd, err)
			}
			if baseBranch != "" {
				task, err = d.SetBaseBranch(task.ID, baseBranch)
				if err != nil {
					exitError(cmd, err)
				}
			}

			if jsonFlag(cmd) {
				printJSON(map[string]string{"id": task.ID})
			} else {
				cmd.Println(task.ID)
			}
		},
	}

	cmd.Flags().StringP("desc", "d", "", "task description")
	cmd.Flags().StringP("parent", "p", "", "parent task ID")
	cmd.Flags().String("after", "", "blocker task ID (new task is blocked by this)")
	cmd.Flags().StringP("repo", "r", "", "repository path for the task")
	cmd.Flags().String("agent", "", "agent CLI for this task: claude or codex")
	cmd.Flags().String("base-branch", "", "branch the task must start from")

	return cmd
}
