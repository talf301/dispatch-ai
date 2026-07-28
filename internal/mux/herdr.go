// Package mux is a thin wrapper over the herdr CLI (v0.7.x). herdr is
// pre-1.0 and its API churns; everything dispatch needs from it goes through
// the Mux interface so a substrate swap touches one file.
package mux

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Mux is the substrate surface dispatch depends on.
type Mux interface {
	// EnsureWorkspace returns the workspace ID labeled `label`, creating it
	// with the given cwd if absent.
	EnsureWorkspace(label, cwd string) (string, error)
	// CreateTab makes a tab in a workspace and returns (tabID, paneID).
	CreateTab(workspaceID, cwd, label string) (string, string, error)
	// RunPane runs a command in a pane.
	RunPane(paneID string, argv []string) error
	// FocusTab focuses a tab in the herdr window.
	FocusTab(tabID string) error
	// RenameTab updates a tab's label.
	RenameTab(tabID, label string) error
	// AgentStates returns pane ID → agent status (idle/working/blocked/done/unknown).
	AgentStates() (map[string]string, error)
	// CurrentPane returns (workspaceID, tabID, paneID, cwd) of the focused pane.
	CurrentPane() (ws, tab, pane, cwd string, err error)
}

// Herdr shells out to the herdr binary, which talks to the server socket and
// prints one JSON object per command.
type Herdr struct{}

func (Herdr) run(out any, args ...string) error {
	raw, err := exec.Command("herdr", args...).Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return fmt.Errorf("herdr %s: %s", strings.Join(args, " "), msg)
	}
	// Some commands (pane run) print nothing on success.
	if out == nil {
		return nil
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("herdr %s: bad JSON: %w", args[0], err)
	}
	if err := json.Unmarshal(envelope.Result, out); err != nil {
		return fmt.Errorf("herdr %s: bad result: %w", args[0], err)
	}
	return nil
}

func (h Herdr) EnsureWorkspace(label, cwd string) (string, error) {
	var list struct {
		Workspaces []struct {
			WorkspaceID string `json:"workspace_id"`
			Label       string `json:"label"`
		} `json:"workspaces"`
	}
	if err := h.run(&list, "workspace", "list"); err != nil {
		return "", err
	}
	for _, w := range list.Workspaces {
		if w.Label == label {
			return w.WorkspaceID, nil
		}
	}
	var created struct {
		Workspace struct {
			WorkspaceID string `json:"workspace_id"`
		} `json:"workspace"`
	}
	if err := h.run(&created, "workspace", "create",
		"--label", label, "--cwd", cwd, "--no-focus"); err != nil {
		return "", err
	}
	return created.Workspace.WorkspaceID, nil
}

func (h Herdr) CreateTab(workspaceID, cwd, label string) (string, string, error) {
	var created struct {
		Tab struct {
			TabID string `json:"tab_id"`
		} `json:"tab"`
		RootPane struct {
			PaneID string `json:"pane_id"`
		} `json:"root_pane"`
	}
	if err := h.run(&created, "tab", "create",
		"--workspace", workspaceID, "--cwd", cwd, "--label", label, "--no-focus"); err != nil {
		return "", "", err
	}
	return created.Tab.TabID, created.RootPane.PaneID, nil
}

func (h Herdr) RunPane(paneID string, argv []string) error {
	args := append([]string{"pane", "run", paneID}, argv...)
	return h.run(nil, args...)
}

func (h Herdr) FocusTab(tabID string) error {
	return h.run(nil, "tab", "focus", tabID)
}

func (h Herdr) RenameTab(tabID, label string) error {
	return h.run(nil, "tab", "rename", tabID, label)
}

func (h Herdr) AgentStates() (map[string]string, error) {
	var list struct {
		Agents []struct {
			PaneID      string `json:"pane_id"`
			AgentStatus string `json:"agent_status"`
		} `json:"agents"`
	}
	if err := h.run(&list, "agent", "list"); err != nil {
		return nil, err
	}
	states := make(map[string]string, len(list.Agents))
	for _, a := range list.Agents {
		states[a.PaneID] = a.AgentStatus
	}
	return states, nil
}

func (h Herdr) CurrentPane() (string, string, string, string, error) {
	var cur struct {
		Pane struct {
			WorkspaceID string `json:"workspace_id"`
			TabID       string `json:"tab_id"`
			PaneID      string `json:"pane_id"`
			Cwd         string `json:"cwd"`
		} `json:"pane"`
	}
	if err := h.run(&cur, "pane", "current"); err != nil {
		return "", "", "", "", err
	}
	p := cur.Pane
	return p.WorkspaceID, p.TabID, p.PaneID, p.Cwd, nil
}
