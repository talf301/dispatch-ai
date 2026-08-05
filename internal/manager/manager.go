// Package manager owns the one human-facing conversational Dispatch session.
// It is a thin adapter: tasks and lifecycle remain in db/dispatchd.
package manager

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/mux"
)

const eventPollInterval = 250 * time.Millisecond

const (
	workspaceKey = "manager.workspace"
	tabKey       = "manager.tab"
	paneKey      = "manager.pane"
	tuiPaneKey   = "manager.tui_pane"
	seenPrefix   = "manager.seen."
)

type Manager struct {
	db  *db.DB
	mux mux.Mux
}

func New(database *db.DB, substrate mux.Mux) *Manager { return &Manager{db: database, mux: substrate} }

// Start recovers the manager pane after a daemon/herdr restart, or creates it
// once. It does not send a model turn until a human message or task event.
func (m *Manager) Start(cwd string) error {
	if m.mux == nil {
		return fmt.Errorf("manager requires herdr")
	}
	ws, _, err := m.db.GetMeta(workspaceKey)
	if err != nil {
		return err
	}
	tab, _, err := m.db.GetMeta(tabKey)
	if err != nil {
		return err
	}
	pane, _, err := m.db.GetMeta(paneKey)
	if err != nil {
		return err
	}
	tuiPane, _, err := m.db.GetMeta(tuiPaneKey)
	if err != nil {
		return err
	}
	states, err := m.mux.AgentStates()
	if err != nil {
		return err
	}
	if ws == "" || tab == "" || pane == "" || !hasPane(states, pane) {
		ws, err = m.mux.EnsureWorkspace("dispatch", cwd)
		if err != nil {
			return err
		}
		tab, pane, err = m.mux.CreateTab(ws, cwd, "Dispatch manager")
		if err != nil {
			return err
		}
		if err := m.mux.RunPane(pane, []string{"claude", "--append-system-prompt", prompt}); err != nil {
			return err
		}
		tuiPane, err = m.startTUI(pane)
		if err != nil {
			return err
		}
		for key, value := range map[string]string{workspaceKey: ws, tabKey: tab, paneKey: pane} {
			if err := m.db.SetMeta(key, value); err != nil {
				return err
			}
		}
		if err := m.db.SetMeta(tuiPaneKey, tuiPane); err != nil {
			return err
		}
	} else if tuiPane == "" {
		tuiPane, err = m.startTUI(pane)
		if err != nil {
			return err
		}
		if err := m.db.SetMeta(tuiPaneKey, tuiPane); err != nil {
			return err
		}
	} else {
		valid, err := m.mux.PaneExists(tuiPane)
		if err != nil {
			return err
		}
		if !valid {
			tuiPane, err = m.startTUI(pane)
			if err != nil {
				return err
			}
			if err := m.db.SetMeta(tuiPaneKey, tuiPane); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) startTUI(managerPane string) (string, error) {
	tuiPane, err := m.mux.SplitPane(managerPane, "right")
	if err != nil {
		return "", err
	}
	dtBin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if err := m.mux.RunPane(tuiPane, []string{dtBin, "tui"}); err != nil {
		return "", err
	}
	return tuiPane, nil
}

// Run listens for local ledger events and polls durable transition notes from
// other processes. The caller owns the lifetime; unchanged periods produce no
// model activity.
func (m *Manager) Run(ctx context.Context) error {
	events, cancel := m.db.Subscribe()
	defer cancel()
	wakeCursor, err := m.db.LatestNoteID()
	if err != nil {
		return err
	}
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case event, ok := <-events:
			if !ok {
				return nil
			}
			if !actionable(event.NewStatus) {
				continue
			}
			if err := m.Notify(event.TaskID); err != nil {
				log.Printf("manager: notify %s: %v", event.TaskID, err)
			}
		case <-ticker.C:
			transitions, nextCursor, err := m.db.ActionableTransitionsAfter(wakeCursor)
			if err != nil {
				log.Printf("manager: read actionable transitions: %v", err)
				continue
			}
			wakeCursor = nextCursor
			for _, event := range transitions {
				if err := m.Notify(event.TaskID); err != nil {
					log.Printf("manager: notify %s: %v", event.TaskID, err)
				}
			}
		}
	}
}

// Notify sends one bounded summary for a decision-relevant transition. The
// status/timestamp cursor is durable, so a restart cannot duplicate it.
func (m *Manager) Notify(taskID string) error {
	task, err := m.db.GetTaskV2(taskID)
	if err != nil {
		return err
	}
	key := seenPrefix + taskID
	cursor := task.Status + "@" + task.UpdatedAt
	seen, _, err := m.db.GetMeta(key)
	if err != nil {
		return err
	}
	if seen == cursor {
		return nil
	}
	pane, _, err := m.db.GetMeta(paneKey)
	if err != nil {
		return err
	}
	if pane == "" {
		return fmt.Errorf("manager pane is not started")
	}
	summary, err := m.summary(task)
	if err != nil {
		return err
	}
	if err := m.mux.PromptAgent(pane, "Actionable Dispatch update:\n"+summary+"\nAsk the human for a decision when needed; do not poll workers."); err != nil {
		return err
	}
	return m.db.SetMeta(key, cursor)
}

func (m *Manager) summary(task *db.Task) (string, error) {
	children, err := m.db.GetChildren(task.ID)
	if err != nil {
		return "", err
	}
	if len(children) > 8 {
		children = children[:8]
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s -> %s", task.ID, task.Title, task.Status)
	if task.ParentID != nil {
		fmt.Fprintf(&b, "; parent: %s", *task.ParentID)
	}
	if task.BlockReason != nil {
		fmt.Fprintf(&b, "; reason: %s", bounded(*task.BlockReason, 240))
	}
	if len(children) > 0 {
		b.WriteString("; children:")
		for _, child := range children {
			fmt.Fprintf(&b, " %s=%s", child.ID, child.Status)
		}
	}
	return bounded(b.String(), 1000), nil
}

func actionable(status string) bool {
	return status == "blocked" || status == "done" || status == "killed" || status == "proposed"
}
func hasPane(states map[string]string, pane string) bool { _, ok := states[pane]; return ok }
func bounded(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var prompt = `You are the single human-facing Dispatch manager. Use dt to capture and propose tasks, and explain bounded ledger summaries. Dispatchd owns merge, retry, block, cleanup, and lifecycle transitions. Never poll workers, validation, CI, or the database through model turns. Wake only for human input or actionable task updates, then end your turn when idle. Ask the human for decisions instead of making lifecycle decisions yourself.`
