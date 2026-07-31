package commands

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func NewUsageCmd() *cobra.Command {
	return &cobra.Command{
		Use: "usage [task-id]", Short: "Show agent usage",
		Args: cobra.MaximumNArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()
			id := ""
			if len(args) == 1 {
				id = args[0]
			}
			r, err := d.Usage(id)
			if err != nil {
				exitError(cmd, err)
			}
			if jsonFlag(cmd) {
				_ = json.NewEncoder(cmd.OutOrStdout()).Encode(r)
				return
			}
			renderUsage(cmd.OutOrStdout(), id, r)
		},
	}
}

func renderUsage(w io.Writer, id string, r *db.UsageReport) {
	fmt.Fprintf(w, "TASK %s\n", id)
	fmt.Fprintln(w, "ID  ROLE      PROVIDER  MODEL  INPUT  CACHED  OUTPUT  REASONING  TURNS  STATUS")
	for _, a := range r.Attempts {
		model := "-"
		if a.Model != nil {
			model = *a.Model
		}
		status := "running"
		if a.ExitStatus != nil {
			status = fmt.Sprint(*a.ExitStatus)
		}
		fmt.Fprintf(w, "%d  %-9s %-9s %-6s %-6d %-7d %-7d %-10d %-6d %s\n", a.ID, a.Role, a.Provider, model, value64(a.InputTokens), value64(a.CachedInputTokens), value64(a.OutputTokens), value64(a.ReasoningTokens), valueInt(a.TurnCount), status)
	}
	fmt.Fprintf(w, "TOTAL attempts=%d input=%d cached=%d output=%d reasoning=%d turns=%d tool_output_bytes=%d\n", r.Totals.Attempts, r.Totals.InputTokens, r.Totals.CachedInputTokens, r.Totals.OutputTokens, r.Totals.ReasoningTokens, r.Totals.Turns, r.Totals.ToolOutputBytes)
}

func value64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func valueInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
