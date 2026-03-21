package commands

import (
	"github.com/spf13/cobra"
)

// NewShowCmd returns the cobra command for showing a task.
func NewShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show task details",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.GetTask(args[0])
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

	return cmd
}
