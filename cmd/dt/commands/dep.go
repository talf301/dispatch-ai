package commands

import (
	"github.com/spf13/cobra"
)

// NewDepCmd returns the cobra command for adding a dependency.
func NewDepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dep <task> <depends-on>",
		Short: "Add a dependency (task depends on depends-on)",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			// CLI order: dep <dependent> <blocker>
			// DB order: AddDep(blocker, blocked)
			if err := d.AddDep(args[1], args[0]); err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(map[string]string{"status": "ok"})
			} else {
				cmd.Println("ok")
			}
		},
	}
}

// NewUndepCmd returns the cobra command for removing a dependency.
func NewUndepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "undep <task> <depends-on>",
		Short: "Remove a dependency",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			// CLI order: undep <dependent> <blocker>
			// DB order: RemoveDep(blocker, blocked)
			if err := d.RemoveDep(args[1], args[0]); err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(map[string]string{"status": "ok"})
			} else {
				cmd.Println("ok")
			}
		},
	}
}
