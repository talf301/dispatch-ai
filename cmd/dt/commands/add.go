package commands

import (
	"github.com/spf13/cobra"
)

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

			var repo *string
			if cmd.Flags().Changed("repo") {
				v, _ := cmd.Flags().GetString("repo")
				repo = &v
			}

			task, err := d.AddTask(title, desc, parent, after, repo)
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
