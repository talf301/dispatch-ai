package commands

import (
	"strings"
	"testing"
)

func TestWarnIfOrphanFromLivePlan(t *testing.T) {
	d := openTestDB(t)

	liveRepo := "/repo/live-session"
	live, err := d.AddTaskWithStatus("planning session", "", "", "", &liveRepo, "live")
	if err != nil {
		t.Fatal(err)
	}

	t.Run("warns when repo matches a live task and no parent given", func(t *testing.T) {
		w := warnIfOrphanFromLivePlan(d, &liveRepo, "")
		if w == "" {
			t.Fatal("expected a warning, got none")
		}
		if !strings.Contains(w, live.ID) {
			t.Errorf("warning should mention the live task %s, got: %s", live.ID, w)
		}
	})

	t.Run("no warning with a parent given", func(t *testing.T) {
		if w := warnIfOrphanFromLivePlan(d, &liveRepo, "somepid"); w != "" {
			t.Errorf("expected no warning when a parent is given, got: %s", w)
		}
	})

	t.Run("no warning for an unrelated repo", func(t *testing.T) {
		other := "/repo/unrelated"
		if w := warnIfOrphanFromLivePlan(d, &other, ""); w != "" {
			t.Errorf("expected no warning for an unrelated repo, got: %s", w)
		}
	})

	t.Run("no warning with no repo", func(t *testing.T) {
		if w := warnIfOrphanFromLivePlan(d, nil, ""); w != "" {
			t.Errorf("expected no warning with a nil repo, got: %s", w)
		}
	})
}
