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
