package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func BranchExists(repoDir, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", branchName)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

func DetectDefaultBranch(repoDir string) (string, error) {
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1], nil
	}

	cmd = exec.Command("git", "branch", "--show-current")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect default branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "main", nil
	}
	return branch, nil
}

func CreateWorktree(repoDir, wtDir, branchName, baseBranch string) error {
	if baseBranch == "" {
		var err error
		baseBranch, err = DetectDefaultBranch(repoDir)
		if err != nil {
			return err
		}
	}

	var cmd *exec.Cmd
	if BranchExists(repoDir, branchName) {
		// Branch already exists (e.g. from a prior failed attempt) — reuse it.
		cmd = exec.Command("git", "worktree", "add", wtDir, branchName)
	} else {
		cmd = exec.Command("git", "worktree", "add", wtDir, "-b", branchName, baseBranch)
	}
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("create worktree: %w\n%s", err, out)
	}
	return nil
}

// MergeBranch merges sourceBranch into targetBranch using a temporary worktree.
func MergeBranch(repoDir, sourceBranch, targetBranch string) error {
	tmpDir, err := os.MkdirTemp("", "dispatch-merge-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Checkout target branch in temp worktree.
	cmd := exec.Command("git", "worktree", "add", tmpDir, targetBranch)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout target: %w\n%s", err, out)
	}
	defer func() {
		rmCmd := exec.Command("git", "worktree", "remove", tmpDir, "--force")
		rmCmd.Dir = repoDir
		rmCmd.Run()
	}()

	// Merge source into target.
	cmd = exec.Command("git", "merge", sourceBranch, "--no-edit")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		abort := exec.Command("git", "merge", "--abort")
		abort.Dir = tmpDir
		abort.Run()
		return fmt.Errorf("merge conflict: %s into %s:\n%s", sourceBranch, targetBranch, out)
	}
	return nil
}

func RemoveWorktree(repoDir, wtDir, branchName string, deleteBranch bool) error {
	cmd := exec.Command("git", "worktree", "remove", wtDir, "--force")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove worktree: %w\n%s", err, out)
	}

	if deleteBranch {
		cmd = exec.Command("git", "branch", "-D", branchName)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("delete branch %s: %w\n%s", branchName, err, out)
		}
	}
	return nil
}
