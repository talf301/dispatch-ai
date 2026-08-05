package commands

import (
	"fmt"
	"io"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

// NewDoneCmd returns the cobra command for marking a task as done.
func NewDoneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "done <id>",
		Short: "Mark a task as done",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, _, err := d.DoneTask(args[0])
			if err != nil {
				exitError(cmd, err)
			}
			if cleanup, err := d.GetTaskV2(task.ID); err == nil {
				closeTaskTab(cleanup)
				removeTaskWorktree(cleanup)
			}

			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				printTask(task)
			}
		},
	}
}

// NewBlockCmd returns the cobra command for blocking a task.
func NewBlockCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "block <id> <reason>",
		Short: "Block a task with a reason",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.BlockTask(args[0], args[1])
			if err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				printTask(task)
			}
		},
	}
}

// NewReopenCmd returns the cobra command for reopening a task.
func NewReopenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reopen <id>",
		Short: "Reopen a blocked or done task",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			if err := warnReopen(d, args[0], cmd.ErrOrStderr()); err != nil {
				exitError(cmd, err)
			}

			task, err := d.ReopenTask(args[0])
			if err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				printTask(task)
			}
		},
	}
}

func warnReopen(d *db.DB, id string, stderr io.Writer) error {
	kind, err := d.BlockKind(id)
	if err != nil {
		return err
	}
	switch kind {
	case db.BlockKindMergeConflict:
		fmt.Fprintln(stderr, "warning: this block was not caused by the task's own work - reopening will re-run an already-approved worker/review cycle and very likely hit the same merge failure; the fix is a NEW task that merges the conflicting sibling's tip and resolves the conflict, not reopening this one.")
	case db.BlockKindPRCreateFailed:
		fmt.Fprintln(stderr, "warning: check whether this branch's work already landed via another PR (zero diff against base) or whether the base/plan branch itself has gone stale before reopening.")
	}
	return nil
}
