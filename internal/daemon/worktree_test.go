package daemon

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initTestRepo creates a bare-minimum git repo with one commit.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestDetectDefaultBranch(t *testing.T) {
	repo := initTestRepo(t)
	branch, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" && branch != "master" {
		t.Errorf("unexpected default branch: %s", branch)
	}
}

func TestCreateWorktree(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-test")

	err := CreateWorktree(repo, wtDir, "dispatch/test-task", "")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(wtDir); err != nil {
		t.Fatalf("worktree dir not created: %v", err)
	}

	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = wtDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "dispatch/test-task" {
		t.Errorf("worktree branch = %q, want dispatch/test-task", got)
	}
}

func TestRemoveWorktree(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-remove")

	CreateWorktree(repo, wtDir, "dispatch/rm-task", "")

	err := RemoveWorktree(repo, wtDir, "dispatch/rm-task", true)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir still exists after removal")
	}

	cmd := exec.Command("git", "branch", "--list", "dispatch/rm-task")
	cmd.Dir = repo
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) != "" {
		t.Error("branch still exists after removal with deleteBranch=true")
	}
}

func TestRemoveWorktree_KeepBranch(t *testing.T) {
	repo := initTestRepo(t)
	wtDir := filepath.Join(t.TempDir(), "wt-keep")

	CreateWorktree(repo, wtDir, "dispatch/keep-task", "")

	err := RemoveWorktree(repo, wtDir, "dispatch/keep-task", false)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(wtDir); !os.IsNotExist(err) {
		t.Error("worktree dir still exists")
	}
	cmd := exec.Command("git", "branch", "--list", "dispatch/keep-task")
	cmd.Dir = repo
	out, _ := cmd.Output()
	if strings.TrimSpace(string(out)) == "" {
		t.Error("branch was deleted when it should have been kept")
	}
}
