package commands

import (
	"fmt"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func NewReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use: "review", Short: "Show the daemon's latest fleet review digest", Args: cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()
			digest, err := d.LoadReviewDigest()
			if err != nil {
				exitError(cmd, err)
			}
			if jsonFlag(cmd) {
				if digest == nil {
					printJSON(db.ReviewDigest{Findings: []db.ReviewFinding{}})
				} else {
					printJSON(digest)
				}
				return
			}
			fmt.Print(renderReview(digest))
		},
	}
}

func renderReview(digest *db.ReviewDigest) string {
	if digest == nil {
		return "No review scan has run.\n"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "# Dispatch review\n\nScanned: %s\n\n", digest.ScannedAt)
	if len(digest.Findings) == 0 {
		out.WriteString("No findings.\n")
		return out.String()
	}
	out.WriteString("## Findings\n")
	for _, finding := range digest.Findings {
		fmt.Fprintf(&out, "- **%s**: %s\n", finding.Kind, finding.Detail)
	}
	return out.String()
}
