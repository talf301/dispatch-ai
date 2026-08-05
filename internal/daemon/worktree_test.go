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

func TestDetectDefaultBranch_IgnoresCheckedOutBranch(t *testing.T) {
	repo := initTestRepo(t)

	// A feature branch checked out must not become "the default branch".
	cmd := exec.Command("git", "checkout", "-b", "feature/wip")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v\n%s", err, out)
	}

	branch, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" && branch != "master" {
		t.Errorf("default branch = %q, want main or master", branch)
	}

	// With no main/master and no origin, it must fail rather than guess.
	for _, name := range []string{"main", "master"} {
		cmd := exec.Command("git", "branch", "-D", name)
		cmd.Dir = repo
		cmd.Run()
	}
	if got, err := DetectDefaultBranch(repo); err == nil {
		t.Errorf("got %q, want an error when no default branch can be determined", got)
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

// The target branch is normally the default branch, which the developer's own
// checkout usually sits on. Merging must not require checking it out.
func TestMergeBranch_TargetCheckedOutInMainWorktree(t *testing.T) {
	repo := initTestRepo(t)

	targetBranch, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}

	featureBranch := "dispatch/feature-checked-out"
	featureWT := filepath.Join(t.TempDir(), "wt-feature")
	if err := CreateWorktree(repo, featureWT, featureBranch, targetBranch); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(featureWT, "feature.txt"), []byte("hello"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "feature.txt"},
		{"git", "commit", "-m", "add feature"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = featureWT
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	RemoveWorktree(repo, featureWT, featureBranch, false)

	if err := MergeBranch(repo, featureBranch, targetBranch); err != nil {
		t.Fatalf("MergeBranch into checked-out %s failed: %v", targetBranch, err)
	}

	cmd := exec.Command("git", "show", targetBranch+":feature.txt")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("feature.txt not found on %s: %v", targetBranch, err)
	}
	if strings.TrimSpace(string(out)) != "hello" {
		t.Errorf("feature.txt = %q, want %q", out, "hello")
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

func TestWorktreeBranchHasCommits(t *testing.T) {
	repo := initTestRepo(t)
	// Reflogs off: the old reflog-based check reported "no commits" here.
	cmd := exec.Command("git", "config", "core.logAllRefUpdates", "false")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git config: %v\n%s", err, out)
	}

	base, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}

	branch := "dispatch/has-commits"
	wtDir := filepath.Join(t.TempDir(), "wt")
	if err := CreateWorktree(repo, wtDir, branch, base); err != nil {
		t.Fatal(err)
	}

	has, err := worktreeBranchHasCommits(wtDir, base, branch)
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("fresh worktree branch should have no commits")
	}

	os.WriteFile(filepath.Join(wtDir, "work.txt"), []byte("work"), 0o644)
	for _, args := range [][]string{
		{"git", "add", "work.txt"},
		{"git", "commit", "-m", "work"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = wtDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	has, err = worktreeBranchHasCommits(wtDir, base, branch)
	if err != nil {
		t.Fatal(err)
	}
	if !has {
		t.Error("branch with a commit should report commits even with reflogs disabled")
	}

	// A missing base ref must surface as an error, not as "no commits".
	if _, err := worktreeBranchHasCommits(wtDir, "no/such/branch", branch); err == nil {
		t.Error("expected an error for a missing base branch")
	}
}

func TestFetchAndFastForwardPlanBranch(t *testing.T) {
	repo := initTestRepo(t)
	base, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}

	plan := "dispatch/plan-test"
	child := "dispatch/child-test"
	for _, branch := range []string{plan, child} {
		cmd := exec.Command("git", "branch", branch, base)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create %s: %v\n%s", branch, err, out)
		}
	}
	cmd := exec.Command("git", "commit", "--allow-empty", "-m", "advance base")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("advance base: %v\n%s", err, out)
	}

	moved, err := FastForwardPlanBranch(repo, plan, base, []string{child})
	if err != nil || !moved {
		t.Fatalf("fast-forward = (%v, %v), want (true, nil)", moved, err)
	}
	baseTip, _ := revParse(repo, base)
	for _, branch := range []string{plan, child} {
		tip, _ := revParse(repo, branch)
		if tip != baseTip {
			t.Errorf("%s tip = %s, want %s", branch, tip, baseTip)
		}
	}

	uniquePlan := "dispatch/plan-unique"
	uniqueWT := filepath.Join(t.TempDir(), "unique")
	if err := CreateWorktree(repo, uniqueWT, uniquePlan, base); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "unique plan work")
	cmd.Dir = uniqueWT
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unique commit: %v\n%s", err, out)
	}
	if err := RemoveWorktree(repo, uniqueWT, uniquePlan, false); err != nil {
		t.Fatal(err)
	}
	uniqueTip, _ := revParse(repo, uniquePlan)
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "advance base again")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("advance base again: %v\n%s", err, out)
	}
	moved, err = FastForwardPlanBranch(repo, uniquePlan, base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if moved {
		t.Error("unique plan was fast-forwarded")
	}
	got, _ := revParse(repo, uniquePlan)
	if got != uniqueTip {
		t.Errorf("unique plan tip changed from %s to %s", uniqueTip, got)
	}
}

func TestFetchOriginBranchSkipsLocalOnlyBranch(t *testing.T) {
	repo := initTestRepo(t)
	remote := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "clone", "--bare", repo, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "branch", "local-only")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create local branch: %v\n%s", err, out)
	}
	if err := FetchOriginBranch(repo, "local-only"); err != nil {
		t.Fatalf("fetch local-only branch: %v", err)
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
