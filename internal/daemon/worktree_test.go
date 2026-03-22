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

func TestMergeBranch_CleanMerge(t *testing.T) {
	repo := initTestRepo(t)

	// Create a target branch (not checked out in main worktree).
	targetBranch := "dispatch/plan-target"
	targetWT := filepath.Join(t.TempDir(), "wt-target")
	if err := CreateWorktree(repo, targetWT, targetBranch, ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(repo, targetWT, targetBranch, false); err != nil {
		t.Fatal(err)
	}

	// Create a feature branch with a new file.
	featureBranch := "dispatch/feature-1"
	featureWT := filepath.Join(t.TempDir(), "wt-feature")
	if err := CreateWorktree(repo, featureWT, featureBranch, ""); err != nil {
		t.Fatal(err)
	}

	// Add a file on the feature branch.
	if err := os.WriteFile(filepath.Join(featureWT, "feature.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "feature.txt"},
		{"git", "commit", "-m", "add feature"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = featureWT
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v failed: %v\n%s", args, err, out)
		}
	}

	// Remove the feature worktree (keep branch).
	if err := RemoveWorktree(repo, featureWT, featureBranch, false); err != nil {
		t.Fatal(err)
	}

	// Merge feature into target.
	if err := MergeBranch(repo, featureBranch, targetBranch); err != nil {
		t.Fatalf("MergeBranch failed: %v", err)
	}

	// Verify the file exists on the target branch.
	cmd := exec.Command("git", "show", targetBranch+":feature.txt")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("feature.txt not found on %s: %v", targetBranch, err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("feature.txt content = %q, want %q", string(out), "hello")
	}
}

func TestMergeBranch_Conflict(t *testing.T) {
	repo := initTestRepo(t)

	// Create a target branch (not checked out in main worktree).
	targetBranch := "dispatch/plan-conflict"
	targetWT := filepath.Join(t.TempDir(), "wt-target")
	if err := CreateWorktree(repo, targetWT, targetBranch, ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(repo, targetWT, targetBranch, false); err != nil {
		t.Fatal(err)
	}

	// Create branch A and modify a file.
	branchA := "dispatch/branch-a"
	wtA := filepath.Join(t.TempDir(), "wt-a")
	if err := CreateWorktree(repo, wtA, branchA, ""); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wtA, "conflict.txt"), []byte("version A"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "conflict.txt"},
		{"git", "commit", "-m", "branch A change"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wtA
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	RemoveWorktree(repo, wtA, branchA, false)

	// Create branch B and modify the same file differently.
	branchB := "dispatch/branch-b"
	wtB := filepath.Join(t.TempDir(), "wt-b")
	if err := CreateWorktree(repo, wtB, branchB, ""); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wtB, "conflict.txt"), []byte("version B"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "conflict.txt"},
		{"git", "commit", "-m", "branch B change"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wtB
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	RemoveWorktree(repo, wtB, branchB, false)

	// Merge A into target — should succeed.
	if err := MergeBranch(repo, branchA, targetBranch); err != nil {
		t.Fatalf("merge A should succeed: %v", err)
	}

	// Merge B into target — should conflict.
	err := MergeBranch(repo, branchB, targetBranch)
	if err == nil {
		t.Fatal("expected merge conflict error, got nil")
	}
	if !strings.Contains(err.Error(), "merge conflict") {
		t.Errorf("error should contain 'merge conflict', got: %v", err)
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
