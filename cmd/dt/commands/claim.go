package commands

import (
	"github.com/spf13/cobra"
)

// NewClaimCmd returns the cobra command for claiming a task.
func NewClaimCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "claim <id> <assignee>",
		Short: "Claim a task (set assignee, status → active)",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.ClaimTask(args[0], args[1])
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

// NewReleaseCmd returns the cobra command for releasing a task.
func NewReleaseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "release <id>",
		Short: "Release a task (clear assignee, status → open)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.ReleaseTask(args[0])
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
