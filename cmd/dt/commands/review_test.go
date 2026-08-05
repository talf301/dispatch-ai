package commands

import (
	"strings"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestRenderReview(t *testing.T) {
	got := renderReview(&db.ReviewDigest{
		ScannedAt: "2026-08-05T00:00:00Z",
		Findings:  []db.ReviewFinding{{Kind: "orphan_worktree", Detail: "worktree gone1 has no live task row"}},
	})
	for _, want := range []string{"# Dispatch review", "Scanned: 2026-08-05T00:00:00Z", "**orphan_worktree**", "gone1"} {
		if !strings.Contains(got, want) {
			t.Errorf("review output %q does not contain %q", got, want)
		}
	}
}
