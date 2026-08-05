package commands

import (
	"github.com/dispatch-ai/dispatch/internal/manager"
	"github.com/dispatch-ai/dispatch/internal/mux"
	"github.com/dispatch-ai/dispatch/internal/secondmate"
	"github.com/spf13/cobra"
)

// NewSecondmateCmd performs one durable blocked-task investigation pass.
func NewSecondmateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "secondmate",
		Short: "Investigate blocked tasks and recover confirmed empty-diff PR failures",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			d := openDB(cmd)
			defer d.Close()
			m := manager.New(d, mux.Herdr{})
			results, err := (&secondmate.Investigator{DB: d, Notify: m.NotifyDecision}).Run()
			if err != nil {
				exitError(cmd, err)
			}
			if jsonFlag(cmd) {
				printJSON(results)
				return
			}
			for _, result := range results {
				cmd.Printf("%s\t%s\t%s\t%s\n", result.TaskID, result.Classification, result.Action, result.Outcome)
			}
		},
	}
}
