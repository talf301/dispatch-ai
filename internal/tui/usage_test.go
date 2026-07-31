package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestUsageViewStates(t *testing.T) {
	model := Model{mode: modeUsage, usage: &usageView{task: &db.UsageReport{
		TaskID:   "abcd",
		Attempts: []db.Attempt{{Provider: "codex", Role: "worker", InputTokens: ptr64Test(12), OutputTokens: ptr64Test(3), TurnCount: ptrTest(2), WaitOnlyCount: ptrTest(1)}},
		Totals:   db.UsageTotals{Attempts: 1, InputTokens: 12, OutputTokens: 3, Turns: 2},
	}}}
	got := model.View()
	for _, want := range []string{"codex", "worker", "raw input 12", "waits 1 (detected)"} {
		if !strings.Contains(got, want) {
			t.Errorf("populated view missing %q: %s", want, got)
		}
	}

	model.usage.task.Attempts[0].WaitOnlyCount = nil
	if got := model.View(); !strings.Contains(got, "waits not recorded") {
		t.Errorf("partial view should distinguish missing wait count: %s", got)
	}

	model.usage.task.Attempts = nil
	if got := model.View(); !strings.Contains(got, "No usage recorded") {
		t.Errorf("missing view should be explicit: %s", got)
	}
}

func TestUsageViewFitsNarrowTerminal(t *testing.T) {
	model := Model{mode: modeUsage, width: 24, usage: &usageView{task: &db.UsageReport{
		TaskID:   "abcd",
		Attempts: []db.Attempt{{Provider: "very-long-provider", Role: "very-long-role"}},
	}}}
	for _, line := range strings.Split(model.View(), "\n") {
		if width := lipgloss.Width(line); width > model.width {
			t.Errorf("line width %d exceeds terminal width %d: %q", width, model.width, line)
		}
	}
}

func ptr64Test(v int64) *int64 { return &v }
func ptrTest(v int) *int       { return &v }
