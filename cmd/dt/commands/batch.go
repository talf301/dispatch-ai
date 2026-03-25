package commands

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

// refPattern matches $1, $2, etc. back-references in batch lines.
var refPattern = regexp.MustCompile(`\$(\d+)`)

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
			var refs []string

			var pending strings.Builder
			pendingStart := 0

			for scanner.Scan() {
				lineNum++
				text := scanner.Text()

				if pending.Len() > 0 {
					// Continuation of a multiline string.
					pending.WriteByte('\n')
					pending.WriteString(text)
					if !quotesBalanced(pending.String()) {
						continue
					}
					text = pending.String()
					pending.Reset()
				} else {
					text = strings.TrimSpace(text)
					// Skip blank lines and comments.
					if text == "" || strings.HasPrefix(text, "#") {
						continue
					}
					if !quotesBalanced(text) {
						pendingStart = lineNum
						pending.WriteString(text)
						continue
					}
				}

				_ = pendingStart // available for error reporting if needed

				resolved, err := substituteRefs(text, refs)
				if err != nil {
					tx.Rollback()
					exitError(cmd, fmt.Errorf("line %d: %s: %w", lineNum, text, err))
				}

				id, err := executeLine(tx, resolved)
				if err != nil {
					tx.Rollback()
					exitError(cmd, fmt.Errorf("line %d: %s: %w", lineNum, text, err))
				}
				if id != "" {
					refs = append(refs, id)
				}
				executed++
			}

			if pending.Len() > 0 {
				tx.Rollback()
				exitError(cmd, fmt.Errorf("line %d: unclosed quote", pendingStart))
			}

			if err := scanner.Err(); err != nil {
				tx.Rollback()
				exitError(cmd, fmt.Errorf("read stdin: %w", err))
			}

			if err := tx.Commit(); err != nil {
				exitError(cmd, fmt.Errorf("commit: %w", err))
			}

			// GraphPilot integration: wire GP graph if GRAPHPILOT_NODE is set.
			// GRAPHPILOT_NODE is the GP node ID of the calling node — set by GP
			// when it spawns a dispatch-planner. This tells GP that the node's
			// work has been decomposed into dispatch tasks.
			if gpNode := os.Getenv("GRAPHPILOT_NODE"); gpNode != "" && len(refs) > 0 {
				parentID := findPlanParent(d, refs)
				if parentID == "" {
					fmt.Fprintf(os.Stderr, "warning: GRAPHPILOT_NODE set but no plan parent found among batch refs; skipping GP wiring\n")
				} else {
					gpBin, err := exec.LookPath("gp")
					if err != nil {
						fmt.Fprintf(os.Stderr, "warning: GRAPHPILOT_NODE set but gp not in PATH; skipping GP wiring\n")
					} else {
						gpCmd := exec.Command(gpBin, "dispatch", gpNode, "--plan", parentID)
						if out, err := gpCmd.CombinedOutput(); err != nil {
							fmt.Fprintf(os.Stderr, "warning: gp dispatch failed: %v\n%s\n", err, string(out))
						} else {
							fmt.Fprintf(os.Stderr, "GP: wired %s to dispatch plan %s\n", gpNode, parentID)
						}
					}
				}
			}

			if jsonFlag(cmd) {
				printJSON(map[string]any{"status": "ok", "lines": executed})
			} else {
				fmt.Printf("ok: %d lines executed\n", executed)
			}
		},
	}
}

// findPlanParent returns the ID of the task in refs that has children (is a
// parent). Returns "" if no parent is found among the refs.
func findPlanParent(database *db.DB, refs []string) string {
	for _, id := range refs {
		task, err := database.GetTask(id)
		if err != nil {
			continue
		}
		if task.ParentID == nil && len(refs) > 1 {
			// A top-level task created alongside others — likely the parent.
			// Confirm by checking if any other ref has this as parent.
			for _, otherID := range refs {
				if otherID == id {
					continue
				}
				other, err := database.GetTask(otherID)
				if err != nil {
					continue
				}
				if other.ParentID != nil && *other.ParentID == id {
					return id
				}
			}
		}
	}
	return ""
}

func executeLine(database *db.DB, line string) (string, error) {
	parts := splitArgs(line)
	if len(parts) == 0 {
		return "", fmt.Errorf("empty command")
	}

	switch parts[0] {
	case "add":
		return batchAdd(database, parts[1:])
	case "edit":
		return "", batchEdit(database, parts[1:])
	case "dep":
		if len(parts) != 3 {
			return "", fmt.Errorf("dep requires 2 arguments: <task> <depends-on>")
		}
		// CLI order: dep <dependent> <blocker>
		// DB order: AddDep(blocker, blocked)
		return "", database.AddDep(parts[2], parts[1])
	case "undep":
		if len(parts) != 3 {
			return "", fmt.Errorf("undep requires 2 arguments: <task> <depends-on>")
		}
		// CLI order: undep <dependent> <blocker>
		// DB order: RemoveDep(blocker, blocked)
		return "", database.RemoveDep(parts[2], parts[1])
	case "claim":
		if len(parts) != 3 {
			return "", fmt.Errorf("claim requires 2 arguments: <id> <assignee>")
		}
		_, err := database.ClaimTask(parts[1], parts[2])
		return "", err
	case "release":
		if len(parts) != 2 {
			return "", fmt.Errorf("release requires 1 argument: <id>")
		}
		_, err := database.ReleaseTask(parts[1])
		return "", err
	case "done":
		if len(parts) != 2 {
			return "", fmt.Errorf("done requires 1 argument: <id>")
		}
		_, _, err := database.DoneTask(parts[1])
		return "", err
	case "block":
		if len(parts) != 3 {
			return "", fmt.Errorf("block requires 2 arguments: <id> <reason>")
		}
		_, err := database.BlockTask(parts[1], parts[2])
		return "", err
	case "reopen":
		if len(parts) != 2 {
			return "", fmt.Errorf("reopen requires 1 argument: <id>")
		}
		_, err := database.ReopenTask(parts[1])
		return "", err
	case "note":
		if len(parts) < 3 {
			return "", fmt.Errorf("note requires at least 2 arguments: <id> <content>")
		}
		author := "batch"
		_, err := database.AddNote(parts[1], strings.Join(parts[2:], " "), &author)
		return "", err
	default:
		return "", fmt.Errorf("unknown command: %s", parts[0])
	}
}

func batchAdd(database *db.DB, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("add requires a title")
	}

	title := ""
	desc := ""
	parent := ""
	after := ""
	var repo *string

	// First non-flag argument is the title.
	i := 0
	for i < len(args) {
		switch args[i] {
		case "-d":
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag -d requires a value")
			}
			desc = args[i+1]
			i += 2
		case "-p":
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag -p requires a value")
			}
			parent = args[i+1]
			i += 2
		case "-r":
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag -r requires a value")
			}
			v := args[i+1]
			repo = &v
			i += 2
		case "--after":
			if i+1 >= len(args) {
				return "", fmt.Errorf("flag --after requires a value")
			}
			after = args[i+1]
			i += 2
		default:
			if title == "" {
				title = args[i]
			} else {
				return "", fmt.Errorf("unexpected argument: %s", args[i])
			}
			i++
		}
	}

	if title == "" {
		return "", fmt.Errorf("add requires a title")
	}

	task, err := database.AddTask(title, desc, parent, after, repo)
	if err != nil {
		return "", err
	}
	return task.ID, nil
}

func batchEdit(database *db.DB, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("edit requires an id")
	}

	id := ""
	var titlePtr, descPtr, repoPtr *string

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
		case "-r":
			if i+1 >= len(args) {
				return fmt.Errorf("flag -r requires a value")
			}
			v := args[i+1]
			repoPtr = &v
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

	_, err := database.EditTask(id, titlePtr, descPtr, repoPtr)
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

// quotesBalanced returns true if all single and double quotes in s are paired.
func quotesBalanced(s string) bool {
	inSingle := false
	inDouble := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'' :
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		}
	}
	return !inSingle && !inDouble
}

// substituteRefs replaces $1, $2, etc. in line with the corresponding IDs from refs.
// References are 1-indexed. Returns an error for out-of-range or zero references.
func substituteRefs(line string, refs []string) (string, error) {
	var errOut error
	result := refPattern.ReplaceAllStringFunc(line, func(match string) string {
		if errOut != nil {
			return match
		}
		numStr := match[1:] // strip leading $
		n, err := strconv.Atoi(numStr)
		if err != nil {
			errOut = fmt.Errorf("invalid back-reference %s", match)
			return match
		}
		if n < 1 {
			errOut = fmt.Errorf("invalid back-reference %s: must be >= $1", match)
			return match
		}
		if n > len(refs) {
			errOut = fmt.Errorf("invalid back-reference %s: only %d add(s) so far", match, len(refs))
			return match
		}
		return refs[n-1]
	})
	if errOut != nil {
		return "", errOut
	}
	return result, nil
}
