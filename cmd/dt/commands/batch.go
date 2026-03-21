package commands

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

// NewBatchCmd returns the cobra command for executing batch operations in a transaction.
func NewBatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "batch",
		Short: "Execute multiple commands in a single transaction (reads from stdin)",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			tx, err := d.BeginTx()
			if err != nil {
				exitError(cmd, fmt.Errorf("begin transaction: %w", err))
			}

			scanner := bufio.NewScanner(os.Stdin)
			lineNum := 0
			executed := 0

			for scanner.Scan() {
				lineNum++
				line := strings.TrimSpace(scanner.Text())

				// Skip blank lines and comments.
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}

				if err := executeLine(tx, line); err != nil {
					tx.Rollback()
					exitError(cmd, fmt.Errorf("line %d: %s: %w", lineNum, line, err))
				}
				executed++
			}

			if err := scanner.Err(); err != nil {
				tx.Rollback()
				exitError(cmd, fmt.Errorf("read stdin: %w", err))
			}

			if err := tx.Commit(); err != nil {
				exitError(cmd, fmt.Errorf("commit: %w", err))
			}

			if jsonFlag(cmd) {
				printJSON(map[string]any{"status": "ok", "lines": executed})
			} else {
				fmt.Printf("ok: %d lines executed\n", executed)
			}
		},
	}
}

func executeLine(database *db.DB, line string) error {
	parts := splitArgs(line)
	if len(parts) == 0 {
		return fmt.Errorf("empty command")
	}

	switch parts[0] {
	case "add":
		return batchAdd(database, parts[1:])
	case "edit":
		return batchEdit(database, parts[1:])
	case "dep":
		if len(parts) != 3 {
			return fmt.Errorf("dep requires 2 arguments: <blocker> <blocked>")
		}
		return database.AddDep(parts[1], parts[2])
	case "undep":
		if len(parts) != 3 {
			return fmt.Errorf("undep requires 2 arguments: <blocker> <blocked>")
		}
		return database.RemoveDep(parts[1], parts[2])
	case "claim":
		if len(parts) != 3 {
			return fmt.Errorf("claim requires 2 arguments: <id> <assignee>")
		}
		_, err := database.ClaimTask(parts[1], parts[2])
		return err
	case "release":
		if len(parts) != 2 {
			return fmt.Errorf("release requires 1 argument: <id>")
		}
		_, err := database.ReleaseTask(parts[1])
		return err
	case "done":
		if len(parts) != 2 {
			return fmt.Errorf("done requires 1 argument: <id>")
		}
		_, err := database.DoneTask(parts[1])
		return err
	case "block":
		if len(parts) != 3 {
			return fmt.Errorf("block requires 2 arguments: <id> <reason>")
		}
		_, err := database.BlockTask(parts[1], parts[2])
		return err
	case "reopen":
		if len(parts) != 2 {
			return fmt.Errorf("reopen requires 1 argument: <id>")
		}
		_, err := database.ReopenTask(parts[1])
		return err
	case "note":
		if len(parts) < 3 {
			return fmt.Errorf("note requires at least 2 arguments: <id> <content>")
		}
		author := "batch"
		_, err := database.AddNote(parts[1], strings.Join(parts[2:], " "), &author)
		return err
	default:
		return fmt.Errorf("unknown command: %s", parts[0])
	}
}

func batchAdd(database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("add requires a title")
	}

	title := ""
	desc := ""
	parent := ""
	after := ""

	// First non-flag argument is the title.
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("flag -d requires a value")
			}
			desc = args[i+1]
			i += 2
		case "-p":
			if i+1 >= len(args) {
				return fmt.Errorf("flag -p requires a value")
			}
			parent = args[i+1]
			i += 2
		case "--after":
			if i+1 >= len(args) {
				return fmt.Errorf("flag --after requires a value")
			}
			after = args[i+1]
			i += 2
		default:
			if title == "" {
				title = args[i]
			} else {
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
			i++
		}
	}

	if title == "" {
		return fmt.Errorf("add requires a title")
	}

	_, err := database.AddTask(title, desc, parent, after)
	return err
}

func batchEdit(database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("edit requires an id")
	}

	id := ""
	var titlePtr, descPtr *string

	i := 0
	for i < len(args) {
		switch args[i] {
		case "-t":
			if i+1 >= len(args) {
				return fmt.Errorf("flag -t requires a value")
			}
			v := args[i+1]
			titlePtr = &v
			i += 2
		case "-d":
			if i+1 >= len(args) {
				return fmt.Errorf("flag -d requires a value")
			}
			v := args[i+1]
			descPtr = &v
			i += 2
		default:
			if id == "" {
				id = args[i]
			} else {
				return fmt.Errorf("unexpected argument: %s", args[i])
			}
			i++
		}
	}

	if id == "" {
		return fmt.Errorf("edit requires an id")
	}

	_, err := database.EditTask(id, titlePtr, descPtr)
	return err
}

// splitArgs splits a line into arguments, respecting single and double quotes.
func splitArgs(line string) []string {
	var args []string
	var current strings.Builder
	inSingle := false
	inDouble := false

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == ' ' && !inSingle && !inDouble:
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
		default:
			current.WriteByte(c)
		}
	}

	if current.Len() > 0 {
		args = append(args, current.String())
	}

	return args
}
