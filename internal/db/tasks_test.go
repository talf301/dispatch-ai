package db

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initBranchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"commit", "--allow-empty", "-qm", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func TestSetBaseBranchValidatesTaskRepo(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "dispatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	repo := initBranchRepo(t)
	task, err := d.AddTask("task", "", "", "", &repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.SetBaseBranch(task.ID, "HEAD"); err != nil {
		t.Fatalf("valid branch rejected: %v", err)
	}

	if _, err := d.SetBaseBranch(task.ID, "does-not-exist"); err == nil || !strings.Contains(err.Error(), "base branch \"does-not-exist\" does not exist") {
		t.Fatalf("invalid branch error = %v", err)
	}
	got, err := d.GetTask(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BaseBranch == nil || *got.BaseBranch != "HEAD" {
		t.Fatalf("base branch after rejected update = %v, want HEAD", got.BaseBranch)
	}
}
