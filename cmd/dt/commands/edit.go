package commands

import (
	"github.com/spf13/cobra"
)

// NewEditCmd returns the cobra command for editing a task.
func NewEditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Edit a task's title or description",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			id := args[0]

			var titlePtr, descPtr *string
			if cmd.Flags().Changed("title") {
				v, _ := cmd.Flags().GetString("title")
				titlePtr = &v
			}
			if cmd.Flags().Changed("desc") {
				v, _ := cmd.Flags().GetString("desc")
				descPtr = &v
			}

			task, err := d.EditTask(id, titlePtr, descPtr)
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

	cmd.Flags().StringP("title", "t", "", "new title")
	cmd.Flags().StringP("desc", "d", "", "new description")

	return cmd
}
