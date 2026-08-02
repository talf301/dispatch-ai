package commands

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// TaskIDCompletion completes task IDs from the local ledger.
func TaskIDCompletion(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 1 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	path, err := cmd.Flags().GetString("db")
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if _, err := os.Stat(path); err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	d := openDB(cmd)
	defer d.Close()
	tasks, err := d.ListTasks("", true)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, t := range tasks {
		if strings.HasPrefix(t.ID, toComplete) {
			out = append(out, t.ID+"\t"+t.Title)
		}
	}
	return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveKeepOrder
}
