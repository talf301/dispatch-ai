package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

func TestCreatePR_ZeroDiffSkipsGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 0)
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", ghDir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })

	d := openTestDB(t)
	repo := repoDir
	parent, err := d.AddTask("already merged", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	headBranch := "dispatch/plan-" + parent.ID
	createPRTestBranch(t, repoDir, headBranch, true)
	daemon := New(d, Config{BaseBranch: "main"}, &MockSpawner{})

	if err := daemon.createPR(repoDir, headBranch, *parent, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was invoked for zero-diff branch: %v", err)
	}
	notes, err := d.GetNotes(parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || !contains(notes[0].Content, "no commits relative to origin/main") {
		t.Fatalf("unexpected zero-diff note: %+v", notes)
	}
	pending, err := d.PendingPRParents()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("zero-diff task remained pending PR: %+v", pending)
	}
}

func TestCreatePR_GitCountErrorFallsThroughToGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 1)
	oldPath := os.Getenv("PATH")
	os.Setenv("PATH", ghDir+string(os.PathListSeparator)+oldPath)
	t.Cleanup(func() { os.Setenv("PATH", oldPath) })

	d := openTestDB(t)
	repo := repoDir
	parent, err := d.AddTask("count error", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	headBranch := "dispatch/plan-" + parent.ID
	createPRTestBranch(t, repoDir, headBranch, false)
	daemon := New(d, Config{BaseBranch: "missing-base"}, &MockSpawner{})

	if err := daemon.createPR(repoDir, headBranch, *parent, true); err == nil || !contains(err.Error(), "gh pr create") {
		t.Fatalf("createPR error = %v, want gh failure", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh was not invoked after git count error: %v", err)
	}
}

func TestCreatePR_DeliveryModes(t *testing.T) {
	for _, mode := range []string{config.DeliveryModeNoMistakes, config.DeliveryModeDirectPR} {
		t.Run(mode, func(t *testing.T) {
			repoDir := initPRTestRepo(t)
			marker := filepath.Join(t.TempDir(), "gh-called")
			ghDir := t.TempDir()
			writeFakeGH(t, ghDir, marker, 0)
			t.Setenv("PATH", ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))

			d := openTestDB(t)
			repo := repoDir
			parent, err := d.AddTask(mode, "", "", "", &repo)
			if err != nil {
				t.Fatal(err)
			}
			headBranch := "dispatch/plan-" + parent.ID
			createPRTestBranch(t, repoDir, headBranch, false)
			if err := os.WriteFile(filepath.Join(repoDir, "pr.txt"), []byte(mode), 0o644); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("git", "add", "pr.txt")
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git add: %v\n%s", err, out)
			}
			cmd = exec.Command("git", "commit", "-m", "plan")
			cmd.Dir = repoDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git commit: %v\n%s", err, out)
			}
			daemon := New(d, Config{BaseBranch: "main", Repos: map[string]config.RepoConfig{repoDir: {Path: repoDir, DeliveryMode: mode}}}, &MockSpawner{})
			if err := daemon.createPR(repoDir, headBranch, *parent, true); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("gh was not invoked: %v", err)
			}
		})
	}
}

func TestCreatePR_LocalOnlyMergesWithoutGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 0)
	t.Setenv("PATH", ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := openTestDB(t)
	repo := repoDir
	parent, err := d.AddTask("local", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	headBranch := "dispatch/plan-" + parent.ID
	createPRTestBranch(t, repoDir, headBranch, false)
	if err := os.WriteFile(filepath.Join(repoDir, "local.txt"), []byte("local"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "add", "local.txt")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "-m", "local")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v\n%s", err, out)
	}

	daemon := New(d, Config{BaseBranch: "main", Repos: map[string]config.RepoConfig{repoDir: {Path: repoDir, DeliveryMode: config.DeliveryModeLocalOnly}}}, &MockSpawner{})
	if err := daemon.createPR(repoDir, headBranch, *parent, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("gh was invoked: %v", err)
	}
	cmd = exec.Command("git", "show", "main:local.txt")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err != nil || string(out) != "local" {
		t.Fatalf("local merge missing from main: %v, %q", err, out)
	}
}

func TestCreatePR_StandaloneZeroDiffFallsThroughToGH(t *testing.T) {
	repoDir := initPRTestRepo(t)
	marker := filepath.Join(t.TempDir(), "gh-called")
	ghDir := t.TempDir()
	writeFakeGH(t, ghDir, marker, 1)
	t.Setenv("PATH", ghDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := openTestDB(t)
	repo := repoDir
	task, err := d.AddTask("standalone already merged", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	headBranch := "dispatch/" + task.ID
	createPRTestBranch(t, repoDir, headBranch, true)
	daemon := New(d, Config{BaseBranch: "main"}, &MockSpawner{})

	if err := daemon.createPR(repoDir, headBranch, *task, false); err == nil || !contains(err.Error(), "gh pr create") {
		t.Fatalf("createPR error = %v, want gh failure", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("gh was not invoked for standalone zero-diff branch: %v", err)
	}
	if _, ok, err := d.GetMeta("pr.handled." + task.ID); err != nil || ok {
		t.Fatalf("standalone zero-diff task was marked handled: ok=%v err=%v", ok, err)
	}
}

func initPRTestRepo(t *testing.T) string {
	t.Helper()
	repoDir := initTestRepo(t)
	for _, args := range [][]string{{"git", "branch", "-M", "main"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "init", "--bare", remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init bare repo: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	return repoDir
}

func createPRTestBranch(t *testing.T, repoDir, headBranch string, mergedToRemote bool) {
	t.Helper()
	cmd := exec.Command("git", "checkout", "-b", headBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create plan branch: %v\n%s", err, out)
	}
	if !mergedToRemote {
		return
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "plan work")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit plan work: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/heads/main", "HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("advance main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "push", "origin", "main")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("push merged main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/heads/main", "HEAD~1")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("restore stale local main: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "update-ref", "refs/remotes/origin/main", "HEAD~1")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stale remote main: %v\n%s", err, out)
	}
}

func writeFakeGH(t *testing.T, dir, marker string, exitCode int) {
	t.Helper()
	path := filepath.Join(dir, "gh")
	script := fmt.Sprintf("#!/bin/sh\nprintf invoked > %q\nexit %d\n", marker, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestFormatPRBody_WithNotes(t *testing.T) {
	worker := "worker"
	notes := []db.Note{
		{Content: "Status changed: open → active", Author: strPtr("system")},
		{Content: "Implemented auth: added auth.go, updated routes.go", Author: &worker},
		{Content: "Added database migration for users table", Author: &worker},
		{Content: "Status changed: active → done", Author: strPtr("system")},
	}

	body := formatPRBody(notes)

	if !contains(body, "## Summary") {
		t.Error("body should contain summary header")
	}
	if !contains(body, "- Implemented auth") {
		t.Error("body should contain worker note 1")
	}
	if !contains(body, "- Added database migration") {
		t.Error("body should contain worker note 2")
	}
	if contains(body, "Status changed") {
		t.Error("body should not contain system notes")
	}
	if !contains(body, "dispatch") {
		t.Error("body should contain dispatch footer")
	}
}

func TestFormatPRBody_NoNotes(t *testing.T) {
	body := formatPRBody(nil)

	if !contains(body, "## Summary") {
		t.Error("body should contain summary header")
	}
	if !contains(body, "_No worker notes recorded._") {
		t.Error("body should contain no-notes message")
	}
}

func TestFormatPRBody_OnlySystemNotes(t *testing.T) {
	notes := []db.Note{
		{Content: "Status changed: open → done", Author: strPtr("system")},
	}

	body := formatPRBody(notes)

	// System notes are filtered out, so no bullets should appear.
	if contains(body, "- Status changed") {
		t.Error("body should not contain system notes as bullets")
	}
}

func TestCheckPendingPRs_NoParents(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := t.TempDir()

	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
	}, &MockSpawner{})

	// Should not panic or error with no pending parents.
	daemon.checkPendingPRs()
}

func TestTriggerPR_UnknownRepo(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := t.TempDir()

	parent, _ := d.AddTask("parent plan", "meta", "", "", nil)

	daemon := New(d, Config{
		Repos:        make(map[string]config.RepoConfig),
		WorktreeBase: worktreeBase,
	}, &MockSpawner{})

	// Should handle gracefully (log warning, no panic).
	daemon.triggerPR(&db.AutoComplete{ParentID: parent.ID})
}

// TestCreatePR_AlreadyExistsIsSuccess proves createPR treats "a PR already
// exists for this head" as success rather than an error. Without this, a
// retry after a daemon restart (or a periodic pending-PR check) would
// erroneously block an already-successfully-PR'd task on its very next
// attempt, just because gh refuses to open a duplicate.
func TestCreatePR_AlreadyExistsIsSuccess(t *testing.T) {
	repoDir := initTestRepo(t)

	bareDir := filepath.Join(t.TempDir(), "origin.git")
	if out, err := exec.Command("git", "clone", "--bare", repoDir, bareDir).CombinedOutput(); err != nil {
		t.Fatalf("clone bare origin: %v\n%s", err, out)
	}
	remoteCmd := exec.Command("git", "remote", "add", "origin", bareDir)
	remoteCmd.Dir = repoDir
	if out, err := remoteCmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	branchCmd := exec.Command("git", "branch", "dispatch/abcd")
	branchCmd.Dir = repoDir
	if out, err := branchCmd.CombinedOutput(); err != nil {
		t.Fatalf("create branch: %v\n%s", err, out)
	}

	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "gh-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\necho 'a pull request for branch \"dispatch/abcd\" into branch \"main\" already exists:' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(binDir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	d := openTestDB(t)
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: t.TempDir(),
	}, &MockSpawner{})

	task, err := d.AddTask("abcd task", "test", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	if err := daemon.createPR(repoDir, "dispatch/abcd", *task, false); err != nil {
		t.Errorf("createPR returned an error for an already-existing PR, want nil: %v", err)
	}
	if _, ok, err := d.GetMeta("pr.handled." + task.ID); err != nil || !ok {
		t.Fatalf("existing PR was not recorded as handled: ok=%v err=%v", ok, err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(string(args), "--base\nmain\n") {
		t.Errorf("gh args = %q, want --base main", args)
	}
}

func strPtr(s string) *string { return &s }

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsHelper(s, substr)
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
