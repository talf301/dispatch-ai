package daemon

import (
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestParseVerdict(t *testing.T) {
	ok, _, err := parseVerdict("I checked the diff.\nVERDICT: approve")
	if err != nil || !ok {
		t.Errorf("approve rejected: %v %v", ok, err)
	}
	ok, reason, err := parseVerdict("Looked at it.\nVERDICT: reject — tests missing for the parser")
	if err != nil || ok || !strings.Contains(reason, "tests missing") {
		t.Errorf("reject mangled: ok=%v reason=%q err=%v", ok, reason, err)
	}
	// No verdict is a hard failure, never an implicit approve (I3).
	if _, _, err := parseVerdict("Looks pretty good to me!"); err == nil {
		t.Error("missing verdict accepted")
	}
}

func TestPromoteGating(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	wt, err := d.CaptureTask("worktree task", "/repo", "worktree")
	if err != nil {
		t.Fatal(err)
	}
	inPlace, _ := d.CaptureTask("in place task", "/repo", "in_place")

	if _, err := d.PromoteTask(inPlace.ID, "report", "done when tests pass"); err == nil {
		t.Error("in-place task promoted")
	}
	if _, err := d.PromoteTask(wt.ID, "report", "  "); err == nil {
		t.Error("empty acceptance accepted")
	}
	if _, err := d.PromoteTask(wt.ID, "vibes", "looks nice"); err == nil {
		t.Error("bogus kind accepted")
	}
	promoted, err := d.PromoteTask(wt.ID, "report", "the viewer renders sample.json")
	if err != nil {
		t.Fatal(err)
	}
	if promoted.Status != "unattended" || *promoted.Acceptance != "the viewer renders sample.json" {
		t.Errorf("promote wrong: %+v", promoted)
	}
	if _, err := d.PromoteTask(wt.ID, "report", "again"); err == nil {
		t.Error("promoting an unattended task should fail")
	}
}

// fakeMux scripts one watcher round: agent settles idle, then (after a
// rejection prompt) settles idle again.
type fakeMux struct {
	mu       sync.Mutex
	prompts  []string
	waits    int
	pane     string
	closed   []string
}

func (f *fakeMux) EnsureWorkspace(label, cwd string) (string, error) { return "w1", nil }
func (f *fakeMux) CreateTab(ws, cwd, label string) (string, string, error) {
	return "w1:t9", "w1:p9", nil
}
func (f *fakeMux) RunPane(pane string, argv []string) error { return nil }
func (f *fakeMux) FocusTab(tab string) error                { return nil }
func (f *fakeMux) RenameTab(tab, label string) error        { return nil }
func (f *fakeMux) CloseTab(tab string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, tab)
	return nil
}
func (f *fakeMux) AgentStates() (map[string]string, error) {
	return map[string]string{f.pane: "idle"}, nil
}
func (f *fakeMux) CurrentPane() (string, string, string, string, error) {
	return "", "", "", "", nil
}
func (f *fakeMux) WaitAgent(pane string, timeout time.Duration, until ...string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waits++
	return "idle", nil
}
func (f *fakeMux) PromptAgent(pane, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prompts = append(f.prompts, text)
	return nil
}

// TestWatchUnattended_RatchetRejectThenBlock: a failing ratchet gets the
// rejection prompted back to the agent, and the cap blocks the task.
func TestWatchUnattended_RatchetRejectThenBlock(t *testing.T) {
	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	workdir := t.TempDir()
	task, err := database.CaptureTask("make it pass", "/repo", "worktree")
	if err != nil {
		t.Fatal(err)
	}
	pane := "w1:p1"
	if err := database.SetRuntime(task.ID, workdir, "w1", "w1:t1", pane); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PromoteTask(task.ID, "ratchet", "exit 1"); err != nil {
		t.Fatal(err)
	}

	fm := &fakeMux{pane: pane}
	d := &Daemon{
		db:                 database,
		mux:                fm,
		logger:             log.New(os.Stderr, "[test] ", 0),
		watchingUnattended: map[string]bool{task.ID: true},
	}
	d.watchUnattended(context.Background(), task.ID)

	got, err := database.GetTaskV2(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "blocked" {
		t.Fatalf("expected blocked after reject cap, got %s", got.Status)
	}
	if got.RejectCount != rejectCap {
		t.Errorf("reject count = %d, want %d", got.RejectCount, rejectCap)
	}
	if len(fm.prompts) != rejectCap-1 {
		t.Errorf("agent prompted %d times, want %d", len(fm.prompts), rejectCap-1)
	}
}

// TestWatchUnattended_RatchetApproveMerges: a passing ratchet merges the
// branch and completes the task.
func TestWatchUnattended_RatchetApproveMerges(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")

	database, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	task, err := database.CaptureTask("ship it", repo, "worktree")
	if err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(t.TempDir(), "wt")
	if err := CreateWorktree(repo, workdir, "dispatch/"+task.ID, "main"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	wrun := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = workdir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	wrun("add", ".")
	wrun("commit", "-qm", "work")

	pane := "w1:p1"
	if err := database.SetRuntime(task.ID, workdir, "w1", "w1:t1", pane); err != nil {
		t.Fatal(err)
	}
	if _, err := database.PromoteTask(task.ID, "ratchet", "test -f b.txt"); err != nil {
		t.Fatal(err)
	}

	fm := &fakeMux{pane: pane}
	d := &Daemon{
		db:                 database,
		mux:                fm,
		baseBranch:         "main",
		logger:             log.New(os.Stderr, "[test] ", 0),
		watchingUnattended: map[string]bool{task.ID: true},
	}
	d.watchUnattended(context.Background(), task.ID)

	got, _ := database.GetTaskV2(task.ID)
	if got.Status != "done" {
		t.Fatalf("expected done, got %s (block: %v)", got.Status, got.BlockReason)
	}
	// The work landed on main.
	cmd := exec.Command("git", "cat-file", "-e", "main:b.txt")
	cmd.Dir = repo
	if err := cmd.Run(); err != nil {
		t.Error("merged file missing from main")
	}
	if len(fm.closed) != 1 {
		t.Errorf("tab not closed: %v", fm.closed)
	}
}
