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

// revParse resolves a revision to a commit SHA in the given directory.
func revParse(dir, rev string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--verify", rev+"^{commit}")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("rev-parse %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeBranch merges sourceBranch into targetBranch using a temporary detached
// worktree, then moves the target ref. It never checks targetBranch out, so it
// works when the target (usually the default branch) is checked out elsewhere.
//
// ponytail: moving the ref leaves any existing checkout of targetBranch with a
// stale index — that checkout will show the merged changes as uncommitted
// reversals until it runs `git reset --hard`/`git checkout .`. Unavoidable
// without touching someone else's working tree.
func MergeBranch(repoDir, sourceBranch, targetBranch string) error {
	// The target tip is both the merge base and the compare-and-swap value
	// used when the ref is moved, so nothing concurrent gets clobbered.
	oldTip, err := revParse(repoDir, targetBranch)
	if err != nil {
		return fmt.Errorf("resolve target %s: %w", targetBranch, err)
	}

	tmpDir, err := os.MkdirTemp("", "dispatch-merge-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	cmd := exec.Command("git", "worktree", "add", "--detach", tmpDir, oldTip)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("checkout target: %w\n%s", err, out)
	}
	defer func() {
		rmCmd := exec.Command("git", "worktree", "remove", tmpDir, "--force")
		rmCmd.Dir = repoDir
		rmCmd.Run()
	}()

	// Merge source into the detached target commit.
	cmd = exec.Command("git", "merge", sourceBranch, "--no-edit")
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		abort := exec.Command("git", "merge", "--abort")
		abort.Dir = tmpDir
		abort.Run()
		return fmt.Errorf("merge conflict: %s into %s:\n%s", sourceBranch, targetBranch, out)
	}

	newTip, err := revParse(tmpDir, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve merge result: %w", err)
	}

	// Only now advance the target ref, and only if it hasn't moved meanwhile.
	cmd = exec.Command("git", "update-ref", "refs/heads/"+targetBranch, newTip, oldTip)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("update %s to merge result: %w\n%s", targetBranch, err, out)
	}
	return nil
}

// worktreeBranchHasCommits checks whether the current branch in wtDir has any
// commits beyond its fork point. Returns false if the worker never committed
// to the worktree branch (e.g. it escaped to the main repo directory).
func worktreeBranchHasCommits(wtDir, branchName string) bool {
	// The reflog for the branch records every commit. Entry 0 is HEAD,
	// and the last entry is the branch creation. If there's more than
	// one entry, the worker made at least one commit.
	cmd := exec.Command("git", "reflog", "show", "--oneline", branchName)
	cmd.Dir = wtDir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return len(lines) > 1
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
