package commands

import (
	"github.com/spf13/cobra"
)

// NewDepCmd returns the cobra command for adding a dependency.
func NewDepCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dep <blocker> <blocked>",
		Short: "Add a dependency (blocker blocks blocked)",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			if err := d.AddDep(args[0], args[1]); err != nil {
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
		Use:   "undep <blocker> <blocked>",
		Short: "Remove a dependency",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			if err := d.RemoveDep(args[0], args[1]); err != nil {
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
