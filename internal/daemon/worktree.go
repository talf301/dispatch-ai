package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func BranchExists(repoDir, branchName string) bool {
	cmd := exec.Command("git", "rev-parse", "--verify", branchName)
	cmd.Dir = repoDir
	return cmd.Run() == nil
}

// FetchOriginBranch refreshes the remote-tracking ref used as a repository
// base. Repositories without an origin are local-only and need no fetch.
func FetchOriginBranch(repoDir, branchName string) error {
	remote := exec.Command("git", "remote", "get-url", "origin")
	remote.Dir = repoDir
	if err := remote.Run(); err != nil {
		return nil
	}
	// A configured local-only base is valid. Do not turn its absent remote
	// counterpart into a permanent spawn/PR failure.
	ref := "refs/heads/" + branchName
	check := exec.Command("git", "ls-remote", "--exit-code", "--heads", "origin", ref)
	check.Dir = repoDir
	if out, err := check.CombinedOutput(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 2 {
			return nil
		}
		return fmt.Errorf("check origin %s: %w\n%s", branchName, err, out)
	}
	cmd := exec.Command("git", "fetch", "origin", branchName)
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git fetch origin %s: %w\n%s", branchName, err, out)
	}
	return nil
}

// DetectDefaultBranch resolves the repository's default branch: origin/HEAD
// first, then a well-known remote branch, then a well-known local branch.
// It deliberately never falls back to the checked-out branch — that silently
// based every worktree on whatever the developer happened to have out. Repos
// that fit none of these need --base-branch (Config.BaseBranch), which every
// caller already prefers over this function.
func DetectDefaultBranch(repoDir string) (string, error) {
	const originPrefix = "refs/remotes/origin/"

	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoDir
	if out, err := cmd.Output(); err == nil {
		ref := strings.TrimSpace(string(out))
		if branch := strings.TrimPrefix(ref, originPrefix); branch != ref && branch != "" {
			return branch, nil
		}
	}

	for _, prefix := range []string{originPrefix, "refs/heads/"} {
		for _, name := range []string{"main", "master"} {
			if BranchExists(repoDir, prefix+name) {
				return name, nil
			}
		}
	}

	return "", fmt.Errorf("detect default branch in %s: no origin/HEAD and no main/master branch — set --base-branch", repoDir)
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

// FastForwardPlanBranch advances a plan branch to newBase only when every
// commit on the plan is already reachable from newBase. Child branches are
// advanced under the same guard; branches with unique work are left alone.
//
// ponytail: moving a child ref leaves a running checkout with a stale index;
// the worker must reset or checkout before continuing, as with MergeBranch.
func FastForwardPlanBranch(repoDir, planBranch, newBase string, childBranches []string) (bool, error) {
	oldPlan, err := revParse(repoDir, planBranch)
	if err != nil {
		return false, fmt.Errorf("resolve plan %s: %w", planBranch, err)
	}
	newTip, err := revParse(repoDir, newBase)
	if err != nil {
		return false, fmt.Errorf("resolve base %s: %w", newBase, err)
	}

	cmd := exec.Command("git", "merge-base", "--is-ancestor", oldPlan, newTip)
	cmd.Dir = repoDir
	if err := cmd.Run(); err != nil {
		// Exit status 1 means the plan contains commits not in the base.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, fmt.Errorf("check plan ancestry: %w", err)
	}

	if oldPlan != newTip {
		cmd = exec.Command("git", "update-ref", "refs/heads/"+planBranch, newTip, oldPlan)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("fast-forward %s: %w\n%s", planBranch, err, out)
		}
	}

	for _, childBranch := range childBranches {
		oldChild, err := revParse(repoDir, childBranch)
		if err != nil {
			continue
		}
		cmd = exec.Command("git", "merge-base", "--is-ancestor", oldChild, newTip)
		cmd.Dir = repoDir
		if err := cmd.Run(); err != nil {
			continue
		}
		if oldChild == newTip {
			continue
		}
		cmd = exec.Command("git", "update-ref", "refs/heads/"+childBranch, newTip, oldChild)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return false, fmt.Errorf("fast-forward child %s: %w\n%s", childBranch, err, out)
		}
	}
	return true, nil
}

// worktreeBranchHasCommits reports whether branchName carries any commits that
// baseBranch doesn't, i.e. whether the worker actually committed to its branch
// rather than escaping to the main checkout. Counting commits directly is the
// only reliable check: reflogs can be disabled (core.logAllRefUpdates=false) or
// expired, and amend/reset write reflog entries with no net new commit.
func worktreeBranchHasCommits(wtDir, baseBranch, branchName string) (bool, error) {
	cmd := exec.Command("git", "rev-list", "--count", baseBranch+".."+branchName)
	cmd.Dir = wtDir
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("count commits on %s: %w", branchName, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return false, fmt.Errorf("parse commit count for %s: %w", branchName, err)
	}
	return n > 0, nil
}

func worktreeCurrentBranch(wtDir string) (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	cmd.Dir = wtDir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read worktree branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" {
		return "", fmt.Errorf("worktree is detached")
	}
	return branch, nil
}

// validateWorktree checks the cheap invariants that must hold before a worker
// can spend time on a task. A fresh branch must start exactly at baseBranch;
// reopened tasks are allowed to retain their existing commits.
func validateWorktree(wtDir, branchName, baseBranch string, fresh bool) error {
	branch, err := worktreeCurrentBranch(wtDir)
	if err != nil {
		return err
	}
	if branch != branchName {
		return fmt.Errorf("worktree is on branch %s, expected %s", branch, branchName)
	}
	base, err := revParse(wtDir, baseBranch)
	if err != nil {
		return fmt.Errorf("resolve base branch %s: %w", baseBranch, err)
	}
	if !fresh {
		return nil
	}
	head, err := revParse(wtDir, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve worktree HEAD: %w", err)
	}
	if head != base {
		return fmt.Errorf("fresh worktree starts at %s, expected base %s", head, base)
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
