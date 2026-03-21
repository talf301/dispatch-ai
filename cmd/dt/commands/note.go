package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// NewNoteCmd returns the cobra command for adding a note to a task.
func NewNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note <id> [content]",
		Short: "Add a note to a task",
		Args:  cobra.MinimumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			var content string
			if len(args) > 1 {
				content = strings.Join(args[1:], " ")
			} else {
				data, err := io.ReadAll(os.Stdin)
				if err != nil {
					exitError(cmd, fmt.Errorf("read stdin: %w", err))
				}
				content = strings.TrimSpace(string(data))
			}

			if content == "" {
				exitError(cmd, fmt.Errorf("note content cannot be empty"))
			}

			author, _ := cmd.Flags().GetString("author")
			note, err := d.AddNote(args[0], content, &author)
			if err != nil {
				exitError(cmd, err)
			}

			if jsonFlag(cmd) {
				printJSON(note)
			} else {
				fmt.Printf("Note added to %s\n", args[0])
			}
		},
	}

	cmd.Flags().String("author", "human", "note author")

	return cmd
}
