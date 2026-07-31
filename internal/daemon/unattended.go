package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/agentctx"
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/mux"
)

// M5: the walk-away loop. Unattended v2 tasks keep their interactive claude
// session in a herdr pane; the daemon watches it instead of polling
// (`herdr agent wait` blocks on the socket), and on every settle runs real
// verification against the written acceptance. Invariant I3: herdr's `done`
// badge is an attention signal — the merge gate is the ratchet command or
// the reviewer, never the badge.

// rejectCap is where reject → redo → reject stops burning tokens overnight.
const rejectCap = 3

// waitSlice bounds each blocking herdr wait so the watcher can notice a
// killed/parked task and daemon shutdown between slices.
const waitSlice = 60 * time.Second

// scanUnattended starts a watcher for any unattended task not already
// watched. Called from the daemon tick; cheap (one SELECT).
func (d *Daemon) scanUnattended(ctx context.Context) {
	if d.mux == nil {
		return
	}
	tasks, err := d.db.UnattendedTasks()
	if err != nil {
		d.logger.Printf("unattended: scan: %v", err)
		return
	}
	for i := range tasks {
		t := tasks[i]
		d.mu.Lock()
		watching := d.watchingUnattended[t.ID]
		if !watching {
			d.watchingUnattended[t.ID] = true
		}
		d.mu.Unlock()
		if !watching {
			d.logger.Printf("unattended: watching %s", t.ID)
			go d.watchUnattended(ctx, t.ID)
		}
	}
}

func (d *Daemon) watchUnattended(ctx context.Context, taskID string) {
	defer func() {
		d.mu.Lock()
		delete(d.watchingUnattended, taskID)
		d.mu.Unlock()
	}()

	for {
		if ctx.Err() != nil {
			return
		}
		t, err := d.db.GetTaskV2(taskID)
		if err != nil || t.Status != "unattended" {
			return // killed, parked, or done by a human — not ours anymore
		}

		pane, err := d.ensurePane(t)
		if err != nil {
			d.blockUnattended(t, fmt.Sprintf("could not reach the task's agent pane: %v", err))
			return
		}

		state, err := d.mux.WaitAgent(pane, waitSlice)
		if err != nil {
			// Timeout while the agent works is the normal case; anything else
			// resolves on the next iteration (pane gone → ensurePane).
			continue
		}
		if state == "working" || state == "unknown" {
			continue
		}

		// Agent settled (idle/done/blocked). Verify against the acceptance.
		ok, reason, verr := d.verifyAcceptance(ctx, t)
		if verr != nil {
			d.blockUnattended(t, fmt.Sprintf("verification could not run: %v", verr))
			return
		}
		if ok {
			d.completeUnattended(t)
			return
		}

		n, err := d.db.IncrementRejectCount(t.ID)
		if err != nil {
			d.logger.Printf("unattended: %s: %v", t.ID, err)
			return
		}
		author := "reviewer"
		d.db.AddNote(t.ID, fmt.Sprintf("rejection %d: %s", n, reason), &author)
		if n >= rejectCap {
			d.blockUnattended(t, fmt.Sprintf("rejected %d times; last: %s", n, reason))
			return
		}
		d.logger.Printf("unattended: %s rejected (round %d): %s", t.ID, n, reason)
		if err := d.mux.PromptAgent(pane, redoPrompt(t, reason)); err != nil {
			d.blockUnattended(t, fmt.Sprintf("could not send rejection to the agent: %v", err))
			return
		}
		// Let the agent leave idle before the next settle-wait, or we'd
		// re-verify the same tree immediately.
		d.mux.WaitAgent(pane, 30*time.Second, "working")
	}
}

func redoPrompt(t *db.Task, reason string) string {
	return fmt.Sprintf(
		"The reviewer rejected this work.\n\nRejection: %s\n\nAcceptance condition that must hold: %s\n\nAddress the rejection, commit your changes, and stop when the condition holds.",
		reason, orEmpty(t.Acceptance))
}

// ensurePane returns a live agent pane for the task, creating tab + session
// if herdr lost it (restart, tab closed by hand).
func (d *Daemon) ensurePane(t *db.Task) (string, error) {
	states, err := d.mux.AgentStates()
	if err != nil {
		return "", err
	}
	if t.HerdrPane != nil && *t.HerdrPane != "" {
		if _, alive := states[*t.HerdrPane]; alive {
			return *t.HerdrPane, nil
		}
	}
	if t.Workdir == nil || t.Repo == nil {
		return "", fmt.Errorf("task has no workdir")
	}
	ws, err := d.mux.EnsureWorkspace(filepath.Base(*t.Repo), *t.Repo)
	if err != nil {
		return "", err
	}
	label := t.Title
	if t.Label != nil && *t.Label != "" {
		label = *t.Label
	}
	tab, pane, err := d.mux.CreateTab(ws, *t.Workdir, label)
	if err != nil {
		return "", err
	}
	// A session already lived in this workdir; continue its conversation
	// with the dispatch context reattached.
	if err := d.mux.RunPane(pane, agentctx.ClaudeArgs(t.ID, "--continue")); err != nil {
		return "", err
	}
	if err := d.db.SetRuntime(t.ID, *t.Workdir, ws, tab, pane); err != nil {
		return "", err
	}
	t.HerdrTab, t.HerdrPane = &tab, &pane
	d.logger.Printf("unattended: %s respawned in pane %s", t.ID, pane)
	return pane, nil
}

// verifyAcceptance runs the real gate. ratchet: the acceptance is a command
// that must exit 0 in the workdir. report: a read-only reviewer agent judges
// the written condition and must end with a VERDICT line.
func (d *Daemon) verifyAcceptance(ctx context.Context, t *db.Task) (ok bool, reason string, err error) {
	kind := orEmpty(t.AcceptanceKind)
	accept := orEmpty(t.Acceptance)
	switch kind {
	case "ratchet":
		cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "sh", "-c", accept)
		cmd.Dir = *t.Workdir
		out, err := cmd.CombinedOutput()
		if err == nil {
			return true, "", nil
		}
		tail := string(out)
		if len(tail) > 2000 {
			tail = tail[len(tail)-2000:]
		}
		return false, fmt.Sprintf("ratchet %q failed: %v\n%s", accept, err, tail), nil

	case "report":
		spawner := &CLISpawner{
			Agent:          d.reviewerAgent,
			ReviewerPrompt: acceptanceReviewerPrompt(t),
			OutputLines:    200,
			SessionDir:     d.sessionDir,
			UsageDB:        d.db,
		}
		handle, err := spawner.Spawn(ctx, *t, *t.Workdir, RoleReviewer, "-accept-review")
		if err != nil {
			return false, "", err
		}
		handle.Wait()
		return parseVerdict(handle.Output())

	default:
		return false, "", fmt.Errorf("unknown acceptance kind %q", kind)
	}
}

func acceptanceReviewerPrompt(t *db.Task) string {
	return fmt.Sprintf(`You are a read-only reviewer for an unattended coding task. Do not modify anything.

The developer captured this task verbatim:

  %s

The written acceptance condition — the ONLY thing you are judging:

  %s

Inspect the repository in your working directory: read the code, use git diff/log against the base branch, run read-only checks (tests are allowed). Decide whether the acceptance condition holds for the committed work. Uncommitted changes do not count.

The LAST line of your output must be exactly one of:
VERDICT: approve
VERDICT: reject — <one line: what fails the acceptance condition>`,
		t.Thought, orEmpty(t.Acceptance))
}

// parseVerdict enforces the reviewer's output contract. No verdict line is a
// hard failure (blocked, human triage) — never an implicit approve.
func parseVerdict(output string) (bool, string, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "VERDICT:") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "VERDICT:"))
			if strings.HasPrefix(rest, "approve") {
				return true, "", nil
			}
			if strings.HasPrefix(rest, "reject") {
				reason := strings.TrimLeft(strings.TrimPrefix(rest, "reject"), " —-:")
				if reason == "" {
					reason = "no reason given"
				}
				return false, reason, nil
			}
		}
	}
	tail := output
	if len(tail) > 1000 {
		tail = tail[len(tail)-1000:]
	}
	return false, "", fmt.Errorf("reviewer gave no verdict; output tail:\n%s", tail)
}

// completeUnattended merges the task branch and tears everything down.
func (d *Daemon) completeUnattended(t *db.Task) {
	repoPath := *t.Repo
	branch := "dispatch/" + t.ID
	base := d.baseBranch
	if base == "" {
		var err error
		base, err = DetectDefaultBranch(repoPath)
		if err != nil {
			d.blockUnattended(t, fmt.Sprintf("acceptance holds but merge target unknown: %v", err))
			return
		}
	}
	if err := MergeBranch(repoPath, branch, base); err != nil {
		d.blockUnattended(t, fmt.Sprintf("acceptance holds but merge failed:\n%v", err))
		return
	}
	if _, _, err := d.db.DoneTask(t.ID); err != nil {
		d.logger.Printf("unattended: done %s: %v", t.ID, err)
	}
	if t.HerdrTab != nil && *t.HerdrTab != "" {
		d.mux.CloseTab(*t.HerdrTab)
	}
	if t.Workdir != nil {
		if err := RemoveWorktree(repoPath, *t.Workdir, branch, true); err != nil {
			d.logger.Printf("unattended: cleanup %s: %v", t.ID, err)
		}
	}
	d.gpSyncChild(t.ID)
	d.logger.Printf("unattended: %s merged into %s and completed", t.ID, base)
}

func (d *Daemon) blockUnattended(t *db.Task, reason string) {
	if len(reason) > 4000 {
		reason = reason[:4000]
	}
	if _, err := d.db.BlockTask(t.ID, reason); err != nil {
		d.logger.Printf("unattended: block %s: %v", t.ID, err)
	}
	d.logger.Printf("unattended: %s blocked: %s", t.ID, strings.SplitN(reason, "\n", 2)[0])
}

func orEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// MuxIfAvailable probes herdr once so a machine without it (or with the
// server down) keeps the v1 loop working with unattended dispatch disabled.
func MuxIfAvailable(logger interface{ Printf(string, ...any) }) mux.Mux {
	h := mux.Herdr{}
	if _, err := h.AgentStates(); err != nil {
		logger.Printf("herdr unavailable, unattended v2 dispatch disabled: %v", err)
		return nil
	}
	return h
}
