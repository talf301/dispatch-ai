package commands

import (
	"fmt"
	"os"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func warnIfBlockerIsBlocked(database *db.DB, blockerID string) {
	task, err := database.GetTask(blockerID)
	if err != nil || task.Status != "blocked" {
		return
	}
	reason := "unspecified"
	if task.BlockReason != nil && *task.BlockReason != "" {
		reason = *task.BlockReason
	}
	fmt.Fprintf(os.Stderr, "warning: task %s is currently blocked (reason: %s). This new task will not be schedulable until %s reaches done. If this task is meant to unblock %s, do not use --after; file it without the dependency.\n", blockerID, reason, blockerID, blockerID)
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
			if after != "" {
				warnIfBlockerIsBlocked(d, after)
			}

			var repo *string
			if cmd.Flags().Changed("repo") {
				v, _ := cmd.Flags().GetString("repo")
				repo = &v
			}

			// Agent-facing path: new work is proposed, never auto-dispatched.
			// A human approves it with `dt reopen <id>`.
			task, err := d.AddTaskWithStatus(title, desc, parent, after, repo, "proposed")
			if err != nil {
				exitError(cmd, err)
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

	return cmd
}
