package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// NewPromoteCmd is the live → unattended gate: the one transition that costs
// ceremony, because it's the one where you walk away.
func NewPromoteCmd() *cobra.Command {
	var kind, acceptance string
	cmd := &cobra.Command{
		Use:   "promote <id>",
		Short: "Hand a live task to the daemon (acceptance required)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			// Prompt interactively for whatever wasn't given as a flag.
			reader := bufio.NewReader(os.Stdin)
			if kind == "" {
				if !stdinIsTTY() {
					exitError(cmd, fmt.Errorf("promote requires --kind (report|ratchet)"))
				}
				fmt.Fprint(os.Stderr, "Acceptance kind — report (prose, agent reviewer) or ratchet (command that must exit 0)? [report/ratchet] ")
				line, _ := reader.ReadString('\n')
				kind = strings.TrimSpace(line)
			}
			if acceptance == "" {
				if !stdinIsTTY() {
					exitError(cmd, fmt.Errorf("promote requires --accept"))
				}
				if kind == "ratchet" {
					fmt.Fprint(os.Stderr, "Command that must exit 0 in the workdir: ")
				} else {
					fmt.Fprint(os.Stderr, "When is this done? (the reviewer merges only if this holds): ")
				}
				line, _ := reader.ReadString('\n')
				acceptance = strings.TrimSpace(line)
			}

			task, err := d.PromoteTask(args[0], kind, acceptance)
			if err != nil {
				exitError(cmd, err)
			}
			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				fmt.Printf("%s unattended · %s: %s\n", task.ID, kind, acceptance)
			}
		},
	}
	cmd.Flags().StringVarP(&kind, "kind", "k", "", "acceptance kind: report | ratchet")
	cmd.Flags().StringVarP(&acceptance, "accept", "a", "", "acceptance condition (prose, or command for ratchet)")
	return cmd
}

func stdinIsTTY() bool {
	stat, err := os.Stdin.Stat()
	return err == nil && stat.Mode()&os.ModeCharDevice != 0
}