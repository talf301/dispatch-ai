package commands

import (
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
