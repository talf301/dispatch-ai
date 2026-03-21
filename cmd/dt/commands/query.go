package commands

import (
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

// ShowResult holds the full details for the show command.
type ShowResult struct {
	Task     *db.Task  `json:"task"`
	Notes    []db.Note `json:"notes"`
	Blockers []db.Task `json:"blockers"`
	Blocking []db.Task `json:"blocking"`
	Children []db.Task `json:"children"`
}

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

			notes, err := d.GetNotes(args[0])
			if err != nil {
				exitError(cmd, err)
			}

			blockers, err := d.GetBlockers(args[0])
			if err != nil {
				exitError(cmd, err)
			}

			blocking, err := d.GetBlocking(args[0])
			if err != nil {
				exitError(cmd, err)
			}

			children, err := d.GetChildren(args[0])
			if err != nil {
				exitError(cmd, err)
			}

			result := ShowResult{
				Task:     task,
				Notes:    notes,
				Blockers: blockers,
				Blocking: blocking,
				Children: children,
			}

			if jsonFlag(cmd) {
				printJSON(result)
			} else {
				printShowResult(result)
			}
		},
	}

	return cmd
}

// NewReadyCmd returns the cobra command for listing ready tasks.
func NewReadyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List tasks ready to be worked on",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			tasks, err := d.ReadyTasks()
			if err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(tasks)
			} else {
				printTaskList(tasks)
			}
		},
	}
	return cmd
}

// NewListCmd returns the cobra command for listing tasks.
func NewListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			all, _ := cmd.Flags().GetBool("all")
			status, _ := cmd.Flags().GetString("status")
			tree, _ := cmd.Flags().GetBool("tree")

			tasks, err := d.ListTasks(status, all)
			if err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(tasks)
			} else if tree {
				printTaskTree(tasks)
			} else {
				printTaskList(tasks)
			}
		},
	}

	cmd.Flags().BoolP("all", "a", false, "include done tasks")
	cmd.Flags().Bool("tree", false, "display as tree grouped by parent")
	cmd.Flags().StringP("status", "s", "", "filter by status")

	return cmd
}
