package commands

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dispatch-ai/dispatch/internal/agentctx"
	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/dispatch-ai/dispatch/internal/dedup"
	"github.com/dispatch-ai/dispatch/internal/llm"
	"github.com/dispatch-ai/dispatch/internal/mux"
	"github.com/spf13/cobra"
)

// gitToplevel resolves the repo root containing dir.
func gitToplevel(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git repository", dir)
	}
	return strings.TrimSpace(string(out)), nil
}

func worktreeDir(taskID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dispatch", "wt", taskID)
}

// sessionFilePath is where the session's dispatch context is written. It
// lives outside any repo working directory (including --here captures) so
// captures never leave a dotfile in the user's real project tree.
func sessionFilePath(taskID string) string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dispatch", "sessions", taskID+".md")
}

// NewGoCmd is the capture path: one command, thought to running agent.
func NewGoCmd() *cobra.Command {
	var here bool
	var noDedup bool
	var repoFlag string
	var thoughtFile string
	cmd := &cobra.Command{
		Use:   "go [thought]",
		Short: "Capture a thought and start an agent on it",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return fmt.Errorf("accepts at most one thought argument")
			}
			if len(args) == 0 && thoughtFile == "" {
				return fmt.Errorf("requires a thought argument or --thought-file")
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			thought, err := loadThought(args, thoughtFile)
			if err != nil {
				exitError(cmd, err)
			}
			d := openDB(cmd)
			defer d.Close()

			if !noDedup {
				if err := checkDedup(d, thought); err != nil {
					exitError(cmd, err)
				}
			}

			cwd, err := os.Getwd()
			if err != nil {
				exitError(cmd, err)
			}
			repoPath := repoFlag
			if repoPath == "" {
				repoPath, err = gitToplevel(cwd)
			} else {
				repoPath, err = filepath.Abs(repoPath)
			}
			if err != nil {
				exitError(cmd, err)
			}

			mode := "worktree"
			if here {
				mode = "in_place"
			}
			task, err := d.CaptureTask(thought, repoPath, mode)
			if err != nil {
				exitError(cmd, err)
			}

			workdir := cwd
			if !here {
				workdir = worktreeDir(task.ID)
				branch := "dispatch/" + task.ID
				if err := daemon.CreateWorktree(repoPath, workdir, branch, ""); err != nil {
					// Capture failed at birth — don't leave a ledger row.
					d.DeleteTask(task.ID)
					exitError(cmd, err)
				}
			}

			// herdr topology: workspace = repo, tab = task, pane = agent.
			// A herdr failure is reported but keeps the task: the worktree
			// exists and the session can be adopted later.
			h := mux.Herdr{}
			label := *task.Label
			ws, err := h.EnsureWorkspace(filepath.Base(repoPath), repoPath)
			if err != nil {
				fmt.Fprintln(os.Stderr, "warning: task captured, but herdr failed:", err)
				printTask(task)
				return
			}
			tab, pane, err := h.CreateTab(ws, workdir, label)
			if err != nil {
				fmt.Fprintln(os.Stderr, "warning: task captured, but herdr failed:", err)
				printTask(task)
				return
			}
			if err := d.SetRuntime(task.ID, workdir, ws, tab, pane); err != nil {
				exitError(cmd, err)
			}
			h.FocusTab(tab)
			sessionPath := sessionFilePath(task.ID)
			if err := agentctx.WriteSessionPrompt(sessionPath, task.ID); err != nil {
				fmt.Fprintln(os.Stderr, "warning: task captured, but failed to write session context:", err)
				printTask(task)
				return
			}
			if err := startAgent(h, pane, task.ID, sessionPath); err != nil {
				fmt.Fprintln(os.Stderr, "warning: could not start claude:", err)
			} else if err := h.PromptAgent(pane, thought); err != nil {
				fmt.Fprintln(os.Stderr, "warning: could not send thought to claude:", err)
			}

			// The human is already typing in the pane; the label call runs
			// after focus, so it costs the capture path nothing (M2).
			generateLabel(d, h, task.ID, thought, tab)

			if jsonFlag(cmd) {
				task, _ = d.GetTaskV2(task.ID)
				printJSON(task)
			} else {
				fmt.Printf("%s  %s  → %s\n", task.ID, label, workdir)
			}
		},
	}
	cmd.Flags().BoolVar(&here, "here", false, "run in place (dirty tree, no worktree)")
	cmd.Flags().BoolVar(&noDedup, "no-dedup", false, "skip the similar-closed-work check")
	cmd.Flags().StringVarP(&repoFlag, "repo", "r", "", "repo path (default: inferred from cwd)")
	cmd.Flags().StringVar(&thoughtFile, "thought-file", "", "read the thought from a file")
	return cmd
}

// startAgentAttemptTimeout bounds a single herdr readiness wait so the outer
// deadline below can actually retry. Herdr requires this to be greater than
// 3 seconds.
const startAgentAttemptTimeout = 5 * time.Second

func startAgent(h mux.Mux, pane, taskID, sessionPath string) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	args := []string{"--append-system-prompt-file", sessionPath}
	var lastErr error
	for {
		if err := h.StartAgent("dispatch-"+taskID, "claude", pane, startAgentAttemptTimeout, args); err == nil {
			return nil
		} else {
			lastErr = err
			// StartAgent can time out after launching Claude but before herdr
			// finishes readiness detection. A retry then reports agent_name_taken;
			// query the pane and treat an existing registration as success.
			if _, statusErr := h.AgentStatus(pane); statusErr == nil {
				return nil
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			return fmt.Errorf("pane %s was not ready: %w", pane, lastErr)
		}
	}
}

func loadThought(args []string, path string) (string, error) {
	if path != "" {
		thought, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read thought file: %w", err)
		}
		return string(thought), nil
	}
	if len(args) == 0 {
		return "", fmt.Errorf("requires a thought argument or --thought-file")
	}
	return args[0], nil
}

// checkDedup is the two-stage capture-time dedup (M3). Stage 1 is free and
// local; the judge only runs when retrieval finds something, so the common
// case costs no tokens and no latency.
func checkDedup(d *db.DB, thought string) error {
	closed, err := d.ClosedTasks()
	if err != nil {
		return err
	}
	var pool []dedup.Candidate
	for _, c := range closed {
		pool = append(pool, dedup.Candidate{
			ID: c.ID, Text: c.Text, Status: c.Status,
			Reason: c.KillReason, Closed: c.UpdatedAt,
		})
	}
	cands := dedup.TopCandidates(thought, pool, 5)
	if len(cands) == 0 {
		return nil
	}

	raw, err := llm.Oneshot(dedup.JudgePrompt(thought, cands))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: dedup judge unavailable; starting without duplicate check: %v\n", err)
		return nil
	}
	matches, err := dedup.ParseJudge(raw)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: dedup judge returned invalid output; starting without duplicate check: %v\n", err)
		return nil
	}
	if len(matches) == 0 {
		return nil
	}

	byID := make(map[string]dedup.Candidate, len(cands))
	for _, c := range cands {
		byID[c.ID] = c
	}
	fmt.Fprintln(os.Stderr, "\nSimilar closed work")
	for _, m := range matches {
		c, ok := byID[m.ID]
		if !ok {
			continue
		}
		fmt.Fprintf(os.Stderr, "\n  %s  %q\n        %s %s: %s\n", c.ID, c.Text, c.Status, c.Closed, m.Reason)
	}

	if stat, _ := os.Stdin.Stat(); stat == nil || stat.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("similar closed work found; rerun with --no-dedup to start anyway")
	}
	fmt.Fprint(os.Stderr, "\n  Still start? y/N ")
	var answer string
	fmt.Scanln(&answer)
	if strings.ToLower(strings.TrimSpace(answer)) != "y" {
		return fmt.Errorf("not started")
	}
	return nil
}

// NewAdoptCmd registers a session already running in the current herdr pane.
func NewAdoptCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt <thought>",
		Short: "Register an already-running session with a one-line why",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			h := mux.Herdr{}
			ws, tab, pane, cwd, err := h.CurrentPane()
			if err != nil {
				exitError(cmd, fmt.Errorf("adopt needs a running herdr session: %w", err))
			}
			repoPath, err := gitToplevel(cwd)
			if err != nil {
				repoPath = cwd
			}
			task, err := d.CaptureTask(args[0], repoPath, "in_place")
			if err != nil {
				exitError(cmd, err)
			}
			if err := d.SetRuntime(task.ID, cwd, ws, tab, pane); err != nil {
				exitError(cmd, err)
			}
			h.RenameTab(tab, *task.Label)
			generateLabel(d, h, task.ID, args[0], tab)

			task, _ = d.GetTaskV2(task.ID)
			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				fmt.Printf("%s  adopted pane %s\n", task.ID, pane)
			}
		},
	}
}

// NewKillCmd closes a task without completion. Reason mandatory.
func NewKillCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "kill <id> <reason>",
		Short: "Close a task without completion (reason required)",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.KillTask(args[0], args[1])
			if err != nil {
				exitError(cmd, err)
			}
			closeTaskTab(task)
			removeTaskWorktree(task)
			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				fmt.Printf("%s killed: %s\n", task.ID, args[1])
			}
		},
	}
}

// NewParkCmd shelves a task: tab closed, worktree kept.
func NewParkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "park <id>",
		Short: "Shelve a task without killing it (worktree preserved)",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.ParkTask(args[0])
			if err != nil {
				exitError(cmd, err)
			}
			closeTaskTab(task)
			if jsonFlag(cmd) {
				printJSON(task)
			} else {
				fmt.Printf("%s parked\n", task.ID)
			}
		},
	}
}

// NewResumeCmd brings interactive tasks back and attaches active daemon tasks
// to their current log on demand.
func NewResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <id>",
		Short: "Resume a parked or stale task",
		Args:  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			task, err := d.GetTaskV2(args[0])
			if err != nil {
				exitError(cmd, err)
			}
			if task.Status == "parked" {
				task, err = d.ResumeTask(args[0])
				if err != nil {
					exitError(cmd, err)
				}
			} else if task.Status == "open" && task.Mode != nil {
				task, err = d.ResumeTask(args[0])
				if err != nil {
					exitError(cmd, err)
				}
			} else if task.Status != "live" && task.Status != "active" {
				exitError(cmd, fmt.Errorf("task %s is %s; only parked, live, active, or released capture tasks resume", task.ID, task.Status))
			}
			if task.Workdir == nil || task.Repo == nil {
				fmt.Printf("%s live again (no recorded workdir; start the session yourself)\n", task.ID)
				return
			}
			h := mux.Herdr{}
			if task.HerdrTab != nil && *task.HerdrTab != "" {
				if err := h.FocusTab(*task.HerdrTab); err == nil {
					fmt.Printf("%s already attached in %s\n", task.ID, *task.Workdir)
					return
				}
			}
			if task.HerdrPane != nil {
				if states, stateErr := h.AgentStates(); stateErr == nil {
					if _, alive := states[*task.HerdrPane]; alive {
						if task.HerdrTab != nil {
							_ = h.FocusTab(*task.HerdrTab)
						}
						fmt.Printf("%s already running in %s\n", task.ID, *task.Workdir)
						return
					}
				}
			}
			ws, err := h.EnsureWorkspace(filepath.Base(*task.Repo), *task.Repo)
			if err != nil {
				exitError(cmd, err)
			}
			label := task.Title
			if task.Label != nil {
				label = *task.Label
			}
			tab, pane, err := h.CreateTab(ws, *task.Workdir, label)
			if err != nil {
				exitError(cmd, err)
			}
			if task.Status == "active" {
				logPath, err := latestSessionLog(task.ID)
				if err != nil {
					_ = h.CloseTab(tab)
					exitError(cmd, err)
				}
				if err := h.RunPane(pane, []string{"tail", "-f", logPath}); err != nil {
					_ = h.CloseTab(tab)
					exitError(cmd, err)
				}
				if err := d.SetRuntime(task.ID, *task.Workdir, ws, tab, pane); err != nil {
					_ = h.CloseTab(tab)
					exitError(cmd, err)
				}
				h.FocusTab(tab)
				fmt.Printf("%s attached → %s\n", task.ID, logPath)
				return
			}
			if err := d.SetRuntime(task.ID, *task.Workdir, ws, tab, pane); err != nil {
				exitError(cmd, err)
			}
			// Resume the conversation rather than restating the thought.
			if err := h.RunPane(pane, agentctx.ClaudeArgs(task.ID, "--continue")); err != nil {
				fmt.Fprintln(os.Stderr, "warning: could not start claude:", err)
			}
			h.FocusTab(tab)
			fmt.Printf("%s resumed → %s\n", task.ID, *task.Workdir)
		},
	}
}

func latestSessionLog(taskID string) (string, error) {
	home, _ := os.UserHomeDir()
	matches, err := filepath.Glob(filepath.Join(home, ".dispatch", "sessions", taskID+"*.log"))
	if err != nil {
		return "", err
	}
	var latest string
	var latestAt time.Time
	for _, path := range matches {
		info, err := os.Stat(path)
		if err == nil && (latest == "" || info.ModTime().After(latestAt)) {
			latest, latestAt = path, info.ModTime()
		}
	}
	if latest == "" {
		return "", fmt.Errorf("no session log found for task %s", taskID)
	}
	return latest, nil
}

// generateLabel fires the one label model call (PRD site 1) and syncs the
// row and the herdr tab. Purely cosmetic: any failure or malformed output
// silently keeps the truncation — never block, never retry.
func generateLabel(d *db.DB, h mux.Mux, taskID, thought, tab string) {
	out, err := llm.Oneshot(
		"Compress this task into a label of at most 3 short words for a kanban board. " +
			"Reply with only the label, lowercase, no punctuation:\n\n" + thought)
	if err != nil {
		return
	}
	label := strings.TrimSpace(out)
	if label == "" || len(strings.Fields(label)) > 4 || len(label) > 40 {
		return
	}
	if err := d.SetLabel(taskID, label); err != nil {
		return
	}
	if tab != "" {
		h.RenameTab(tab, label)
	}
}

// NewRelabelCmd fixes a bad label by hand.
func NewRelabelCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "relabel <id> <text>",
		Short: "Replace a task's display label",
		Args:  cobra.ExactArgs(2),
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			if err := d.SetLabel(args[0], args[1]); err != nil {
				exitError(cmd, err)
			}
			task, err := d.GetTaskV2(args[0])
			if err != nil {
				exitError(cmd, err)
			}
			if task.HerdrTab != nil && *task.HerdrTab != "" {
				mux.Herdr{}.RenameTab(*task.HerdrTab, args[1])
			}
			fmt.Printf("%s  %s\n", task.ID, args[1])
		},
	}
}

// closeTaskTab closes the task's herdr tab if it has one. Best-effort: the
// tab may already be gone or the server down.
func closeTaskTab(task *db.Task) {
	if task.HerdrTab == nil || *task.HerdrTab == "" {
		return
	}
	if err := exec.Command("herdr", "tab", "close", *task.HerdrTab).Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not close herdr tab %s\n", *task.HerdrTab)
	}
}

// removeTaskWorktree tears down a task's worktree. Best-effort.
func removeTaskWorktree(task *db.Task) {
	if task.Mode == nil || *task.Mode != "worktree" ||
		task.Workdir == nil || task.Repo == nil {
		return
	}
	if err := daemon.RemoveWorktree(*task.Repo, *task.Workdir, "dispatch/"+task.ID, true); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove worktree %s: %v\n", *task.Workdir, err)
	}
}
