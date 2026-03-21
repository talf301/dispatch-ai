package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

// jsonFlag returns true if --json was set on the root command.
func jsonFlag(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("json")
	return v
}

// printJSON writes v as indented JSON to stdout.
func printJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// printTask prints a single task in key-value format using tabwriter.
func printTask(t *db.Task) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "ID\t%s\n", t.ID)
	fmt.Fprintf(w, "Title\t%s\n", t.Title)
	fmt.Fprintf(w, "Status\t%s\n", t.Status)
	if t.Description != "" {
		fmt.Fprintf(w, "Description\t%s\n", t.Description)
	}
	if t.ParentID != nil {
		fmt.Fprintf(w, "Parent\t%s\n", *t.ParentID)
	}
	if t.Assignee != nil {
		fmt.Fprintf(w, "Assignee\t%s\n", *t.Assignee)
	}
	if t.BlockReason != nil {
		fmt.Fprintf(w, "Block Reason\t%s\n", *t.BlockReason)
	}
	fmt.Fprintf(w, "Created\t%s\n", t.CreatedAt)
	fmt.Fprintf(w, "Updated\t%s\n", t.UpdatedAt)
	w.Flush()
}

// printTaskList prints tasks as a table with headers.
func printTaskList(tasks []db.Task) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tSTATUS\tTITLE\n")
	for _, t := range tasks {
		fmt.Fprintf(w, "%s\t%s\t%s\n", t.ID, t.Status, t.Title)
	}
	w.Flush()
}

// printTaskTree prints tasks as a tree grouped by parent-child relationships.
func printTaskTree(tasks []db.Task) {
	// Build parent -> children map.
	childrenMap := make(map[string][]db.Task)
	taskMap := make(map[string]db.Task)
	for _, t := range tasks {
		taskMap[t.ID] = t
		parentKey := ""
		if t.ParentID != nil {
			parentKey = *t.ParentID
		}
		childrenMap[parentKey] = append(childrenMap[parentKey], t)
	}

	// Find roots: tasks with no parent or whose parent is not in the task set.
	var roots []db.Task
	for _, t := range tasks {
		if t.ParentID == nil {
			roots = append(roots, t)
		} else if _, ok := taskMap[*t.ParentID]; !ok {
			roots = append(roots, t)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "ID\tSTATUS\tTITLE\n")
	var printTree func(tasks []db.Task, indent int)
	printTree = func(tasks []db.Task, indent int) {
		for _, t := range tasks {
			prefix := ""
			for i := 0; i < indent; i++ {
				prefix += "  "
			}
			fmt.Fprintf(w, "%s\t%s\t%s%s\n", t.ID, t.Status, prefix, t.Title)
			if children, ok := childrenMap[t.ID]; ok {
				printTree(children, indent+1)
			}
		}
	}
	printTree(roots, 0)
	w.Flush()
}

// printShowResult prints the full show output with notes, deps, and children.
func printShowResult(r ShowResult) {
	printTask(r.Task)

	if len(r.Blockers) > 0 {
		fmt.Println()
		fmt.Println("Blocked by:")
		for _, t := range r.Blockers {
			fmt.Printf("  %s  %s  (%s)\n", t.ID, t.Title, t.Status)
		}
	}

	if len(r.Blocking) > 0 {
		fmt.Println()
		fmt.Println("Blocking:")
		for _, t := range r.Blocking {
			fmt.Printf("  %s  %s  (%s)\n", t.ID, t.Title, t.Status)
		}
	}

	if len(r.Children) > 0 {
		fmt.Println()
		fmt.Println("Children:")
		for _, t := range r.Children {
			fmt.Printf("  %s  %s  (%s)\n", t.ID, t.Title, t.Status)
		}
	}

	if len(r.Notes) > 0 {
		fmt.Println()
		fmt.Println("Notes:")
		for _, n := range r.Notes {
			author := "unknown"
			if n.Author != nil {
				author = *n.Author
			}
			fmt.Printf("  [%s] %s: %s\n", n.CreatedAt, author, n.Content)
		}
	}
}

// exitError prints the error and exits with code 1.
func exitError(cmd *cobra.Command, err error) {
	if jsonFlag(cmd) {
		printJSON(map[string]string{"error": err.Error()})
	} else {
		fmt.Fprintln(os.Stderr, "error:", err)
	}
	os.Exit(1)
}

// openDB reads the --db flag and opens the database.
func openDB(cmd *cobra.Command) *db.DB {
	path, _ := cmd.Flags().GetString("db")
	d, err := db.Open(path)
	if err != nil {
		exitError(cmd, err)
	}
	return d
}
