package daemon

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/db"
)

// Note: initTestRepo is defined in worktree_test.go (same package).

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func testRepos(repoDir string) map[string]config.RepoConfig {
	return map[string]config.RepoConfig{
		repoDir: {Path: repoDir, MaxWorkers: 4},
	}
}

func TestDaemonConfig_Defaults(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Repos == nil {
		t.Error("Repos map should not be nil")
	}
	if cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval = %v, want 5s", cfg.PollInterval)
	}
}

func TestBaseBranchForTaskOverride(t *testing.T) {
	base := "feature/source"
	d := &Daemon{baseBranch: "main"}
	got, err := d.baseBranchFor(&db.Task{BaseBranch: &base})
	if err != nil || got != base {
		t.Fatalf("baseBranchFor = (%q, %v), want (%q, nil)", got, err, base)
	}
}

func TestTaskRepoPath(t *testing.T) {
	repo := "/repo/one"
	task := &db.Task{ID: "abcd"}

	one := &Daemon{repos: map[string]config.RepoConfig{repo: {Path: repo}}}
	got, err := one.taskRepoPath(task)
	if err != nil || got != repo {
		t.Errorf("single repo: got (%q, %v), want (%q, nil)", got, err, repo)
	}

	two := &Daemon{repos: map[string]config.RepoConfig{
		repo:      {Path: repo},
		"/repo/2": {Path: "/repo/2"},
	}}
	if got, err := two.taskRepoPath(task); err == nil {
		t.Errorf("two repos: got %q, want an error rather than a guess", got)
	}

	explicit := "/repo/2"
	if got, err := two.taskRepoPath(&db.Task{ID: "abcd", Repo: &explicit}); err != nil || got != explicit {
		t.Errorf("explicit repo: got (%q, %v), want (%q, nil)", got, err, explicit)
	}

	none := &Daemon{repos: map[string]config.RepoConfig{}}
	if got, err := none.taskRepoPath(task); err == nil {
		t.Errorf("no repos: got %q, want an error rather than the daemon cwd", got)
	}
}

func TestBaseBranchForUsesOriginRefWhenItIsAhead(t *testing.T) {
	repo := initTestRepo(t)
	base, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(t.TempDir(), "origin.git")
	cmd := exec.Command("git", "clone", "--bare", repo, remote)
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}

	peer := filepath.Join(t.TempDir(), "peer")
	cmd = exec.Command("git", "clone", remote, peer)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone peer: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"git", "config", "user.email", "test@test.com"}, {"git", "config", "user.name", "Test"}, {"git", "commit", "--allow-empty", "-m", "origin advance"}, {"git", "push", "origin", "HEAD:" + base}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = peer
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	cmd = exec.Command("git", "fetch", "origin", base)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fetch: %v\n%s", err, out)
	}

	d := &Daemon{repos: testRepos(repo)}
	ref, err := d.baseBranchFor(&db.Task{ID: "standalone", Repo: &repo})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "origin/"+base {
		t.Fatalf("base ref = %q, want origin/%s", ref, base)
	}

	wt := filepath.Join(t.TempDir(), "worktree")
	if err := CreateWorktree(repo, wt, "dispatch/standalone", ref); err != nil {
		t.Fatal(err)
	}
	has, err := worktreeBranchHasCommits(wt, ref, "dispatch/standalone")
	if err != nil {
		t.Fatal(err)
	}
	if has {
		t.Fatal("fresh worktree incorrectly appears to contain worker commits")
	}
}

func TestDaemonSpawnUsesFetchedBaseForStandaloneAndChild(t *testing.T) {
	database := openTestDB(t)
	repo := initTestRepo(t)
	base, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}

	remote := filepath.Join(t.TempDir(), "origin.git")
	for _, args := range [][]string{{"clone", "--bare", repo, remote}, {"remote", "add", "origin", remote}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	peer := filepath.Join(t.TempDir(), "peer")
	if out, err := exec.Command("git", "clone", remote, peer).CombinedOutput(); err != nil {
		t.Fatalf("clone peer: %v\n%s", err, out)
	}
	for _, args := range [][]string{{"config", "user.email", "test@test.com"}, {"config", "user.name", "Test"}, {"commit", "--allow-empty", "-m", "origin advance"}, {"push", "origin", "HEAD:" + base}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = peer
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	standalone, _ := database.AddTask("standalone", "", "", "", nil)
	parent, _ := database.AddTask("parent", "", "", "", nil)
	child, _ := database.AddTask("child", "", parent.ID, "", nil)
	spawner := &MockSpawner{}
	daemon := New(database, Config{
		Repos:        testRepos(repo),
		WorktreeBase: filepath.Join(t.TempDir(), "worktrees"),
	}, spawner)

	daemon.spawnReady()

	for _, task := range []*db.Task{standalone, child} {
		updated, err := database.GetTask(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if updated.Status != "active" {
			t.Errorf("task %s status = %s, want active", task.ID, updated.Status)
		}
	}
	if len(spawner.Spawned) != 2 {
		t.Fatalf("spawned %d tasks, want standalone and child", len(spawner.Spawned))
	}
}

func TestBaseBranchForRespectsExplicitLocalBase(t *testing.T) {
	repo := initTestRepo(t)
	base, err := DetectDefaultBranch(repo)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "branch", "wip", base)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create wip: %v\n%s", err, out)
	}
	remote := filepath.Join(t.TempDir(), "origin.git")
	cmd = exec.Command("git", "clone", "--bare", repo, remote)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "remote", "add", "origin", remote)
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("add origin: %v\n%s", err, out)
	}
	if err := FetchOriginBranch(repo, "wip"); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command("git", "checkout", "wip")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout wip: %v\n%s", err, out)
	}
	cmd = exec.Command("git", "commit", "--allow-empty", "-m", "local wip advance")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("advance wip: %v\n%s", err, out)
	}

	d := &Daemon{baseBranch: "wip", repos: testRepos(repo)}
	ref, err := d.baseBranchFor(&db.Task{ID: "standalone", Repo: &repo})
	if err != nil {
		t.Fatal(err)
	}
	if ref != "wip" {
		t.Fatalf("base ref = %q, want explicit local branch wip", ref)
	}
}

func TestTaskRepoPathMapsDispatchWorktree(t *testing.T) {
	repo := initTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "worktree")
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-b", "dispatch/test", worktree)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create worktree: %v\n%s", err, out)
	}
	d := &Daemon{repos: testRepos(repo)}
	task := &db.Task{ID: "abcd", Repo: &worktree}
	got, err := d.taskRepoPath(task)
	if err != nil || got != repo {
		t.Fatalf("worktree repo: got (%q, %v), want (%q, nil)", got, err, repo)
	}
}

func TestAdoptedHandle_CleanExitIsNotAFailure(t *testing.T) {
	exitedPID := func(t *testing.T) int {
		t.Helper()
		cmd := exec.Command("true")
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := cmd.Process.Pid
		cmd.Wait()
		return pid
	}

	for _, tc := range []struct {
		name      string
		committed bool
		wantErr   bool
	}{
		{"worker committed", true, false},
		{"worker committed nothing", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newAdoptedHandle(exitedPID(t), func() bool { return tc.committed })
			select {
			case <-h.Done():
			case <-time.After(10 * time.Second):
				t.Fatal("adopted handle never reported exit")
			}
			if gotErr := h.Err() != nil; gotErr != tc.wantErr {
				t.Errorf("Err() = %v, wantErr %v", h.Err(), tc.wantErr)
			}
		})
	}
}

func TestDaemon_RecoverActive_DeadProcess(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("recover test", "", "", "", nil)
	d.ClaimTask(task.ID, "old-session")

	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, "worker.pid"), []byte("99999999"), 0o644)

	daemon := &Daemon{
		db:           d,
		repos:        make(map[string]config.RepoConfig),
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		workerRepo:   make(map[string]string),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "blocked" {
		t.Errorf("status = %s, want blocked", updated.Status)
	}
}

func TestDaemon_RecoverActive_NoWorktree(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("no worktree test", "", "", "", nil)
	d.ClaimTask(task.ID, "old-session")

	daemon := &Daemon{
		db:           d,
		repos:        make(map[string]config.RepoConfig),
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		workerRepo:   make(map[string]string),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "blocked" {
		t.Errorf("status = %s, want blocked", updated.Status)
	}
}

func TestDaemon_RecoverActive_LiveProcess(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("live process test", "", "", "", nil)
	d.ClaimTask(task.ID, "old-session")

	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	if err := writePIDFile(filepath.Join(wtDir, "worker.pid"), os.Getpid()); err != nil {
		t.Fatal(err)
	}

	daemon := &Daemon{
		db:           d,
		repos:        make(map[string]config.RepoConfig),
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		workerRepo:   make(map[string]string),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}
}

func TestDaemon_RecoverActive_ReusedPID(t *testing.T) {
	d := openTestDB(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")
	os.MkdirAll(worktreeBase, 0o755)

	task, _ := d.AddTask("reused pid test", "", "", "", nil)
	d.ClaimTask(task.ID, "old-session")

	// Live pid, but not the process that was recorded.
	wtDir := filepath.Join(worktreeBase, task.ID)
	os.MkdirAll(wtDir, 0o755)
	os.WriteFile(filepath.Join(wtDir, "worker.pid"),
		[]byte(strconv.Itoa(os.Getpid())+"\nWed Jan  1 00:00:00 2020\n"), 0o644)

	daemon := &Daemon{
		db:           d,
		repos:        make(map[string]config.RepoConfig),
		worktreeBase: worktreeBase,
		workers:      make(map[string]WorkerHandle),
		workerRepo:   make(map[string]string),
		logger:       log.New(io.Discard, "", 0),
	}

	daemon.recoverActive()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "blocked" {
		t.Errorf("status = %s, want blocked (pid was recycled)", updated.Status)
	}
	if len(daemon.workers) != 0 {
		t.Errorf("adopted %d unrelated processes, want 0", len(daemon.workers))
	}
}

func TestDaemon_SpawnWorker(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	task, _ := d.AddTask("spawn test", "", "", "", nil)

	spawner := &MockSpawner{ExitCode: 0}
	fm := &fakeMux{}
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
		SessionDir:   t.TempDir(),
		Mux:          fm,
	}, spawner)

	daemon.spawnReady()

	updated, _ := d.GetTask(task.ID)
	if updated.Status != "active" {
		t.Errorf("status = %s, want active", updated.Status)
	}
	runtime, err := d.GetTaskV2(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Workdir == nil || *runtime.Workdir != filepath.Join(worktreeBase, task.ID) {
		t.Fatalf("workdir = %v, want spawned worktree", runtime.Workdir)
	}
	if fm.created != 0 || (runtime.HerdrTab != nil && *runtime.HerdrTab != "") {
		t.Fatalf("automated spawn created herdr runtime: tabs=%d tab=%v", fm.created, runtime.HerdrTab)
	}

	if len(spawner.Spawned) != 1 {
		t.Fatalf("expected 1 spawn, got %d", len(spawner.Spawned))
	}
	if spawner.Spawned[0].ID != task.ID {
		t.Errorf("spawned task ID = %s, want %s", spawner.Spawned[0].ID, task.ID)
	}
}

func TestDaemon_MaxWorkers(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	for i := 0; i < 5; i++ {
		d.AddTask(fmt.Sprintf("task %d", i), "", "", "", nil)
	}

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		Repos: map[string]config.RepoConfig{
			repoDir: {Path: repoDir, MaxWorkers: 2},
		},
		WorktreeBase: worktreeBase,
	}, spawner)

	daemon.spawnReady()

	if len(spawner.Spawned) != 2 {
		t.Errorf("spawned %d tasks, want 2 (max_workers)", len(spawner.Spawned))
	}
}

func TestDaemon_RunAndShutdown(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		PollInterval: 50 * time.Millisecond,
		WorktreeBase: worktreeBase,
	}, spawner)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- daemon.Run(ctx)
	}()

	time.Sleep(200 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil && err != context.Canceled {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down within 5 seconds")
	}
}

func TestDaemon_SpawnChildUsesParentBranch(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	parent, _ := d.AddTask("parent plan", "meta", "", "", nil)
	child, _ := d.AddTask("child task", "do work", parent.ID, "", nil)

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
	}, spawner)

	daemon.spawnReady()

	// Parent should NOT be spawned (has children — excluded by ReadyTasks).
	// Child should be spawned.
	if len(spawner.Spawned) != 1 {
		t.Fatalf("expected 1 spawn, got %d", len(spawner.Spawned))
	}
	if spawner.Spawned[0].ID != child.ID {
		t.Errorf("spawned task = %s, want %s", spawner.Spawned[0].ID, child.ID)
	}

	// Parent branch should exist.
	parentBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
	if !BranchExists(repoDir, parentBranch) {
		t.Errorf("parent branch %s should exist", parentBranch)
	}
}

func TestDaemon_SpawnChildUsesParentExplicitBaseBranch(t *testing.T) {
	database := openTestDB(t)
	repoDir := initTestRepo(t)
	defaultBranch, err := DetectDefaultBranch(repoDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"checkout", "-b", "feature/source"},
		{"commit", "--allow-empty", "-m", "feature base"},
		{"checkout", defaultBranch},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	parent, _ := database.AddTask("parent plan", "", "", "", nil)
	if _, err := database.SetBaseBranch(parent.ID, "feature/source"); err != nil {
		t.Fatal(err)
	}
	_, _ = database.AddTask("child", "", parent.ID, "", nil)
	daemon := New(database, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: filepath.Join(t.TempDir(), "worktrees"),
	}, &MockSpawner{})

	daemon.spawnReady()
	parentTip, err := revParse(repoDir, "dispatch/plan-"+parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	featureTip, err := revParse(repoDir, "feature/source")
	if err != nil {
		t.Fatal(err)
	}
	if parentTip != featureTip {
		t.Fatalf("parent plan starts at %s, want feature base %s", parentTip, featureTip)
	}
}

func TestDaemon_MonitorCleanExit(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	task, _ := d.AddTask("monitor test", "", "", "", nil)

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
	}, spawner)

	daemon.spawnReady()

	// First monitorWorkers detects worker exit (clean, with commits) and spawns reviewer.
	daemon.monitorWorkers()

	// Reviewer should now be in the map (MockSpawner exits 0 immediately).
	if role := daemon.taskRoles[task.ID]; role != RoleReviewer {
		t.Errorf("expected reviewer role, got %q", role)
	}

	// Second monitorWorkers detects reviewer exit (clean) and completes the task.
	daemon.monitorWorkers()

	// Worker should be removed from the map.
	if _, exists := daemon.workers[task.ID]; exists {
		t.Error("worker still in map after clean exit")
	}
}

func TestDaemon_MergeChildOnCompletion(t *testing.T) {
	d := openTestDB(t)
	repoDir := initTestRepo(t)
	worktreeBase := filepath.Join(t.TempDir(), "worktrees")

	// Create parent + child tasks.
	parent, _ := d.AddTask("parent plan", "meta", "", "", nil)
	child, _ := d.AddTask("child task", "do work", parent.ID, "", nil)

	spawner := &MockSpawner{ExitCode: 0}
	daemon := New(d, Config{
		Repos:        testRepos(repoDir),
		WorktreeBase: worktreeBase,
	}, spawner)

	// Spawn ready — this creates the worktree and branch for the child.
	daemon.spawnReady()

	if len(spawner.Spawned) != 1 {
		t.Fatalf("expected 1 spawn, got %d", len(spawner.Spawned))
	}

	// Simulate work: create a file and commit in the child's worktree.
	childWT := filepath.Join(worktreeBase, child.ID)
	if err := os.WriteFile(filepath.Join(childWT, "child-work.txt"), []byte("child output"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "add", "child-work.txt"},
		{"git", "commit", "-m", "child work"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = childWT
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	// Mark child done (simulating worker calling dt done).
	d.DoneTask(child.ID)

	// First monitorWorkers detects worker exit and spawns reviewer.
	daemon.monitorWorkers()

	// Reviewer should now be in the map.
	if role := daemon.taskRoles[child.ID]; role != RoleReviewer {
		t.Errorf("expected reviewer role, got %q", role)
	}

	// Second monitorWorkers detects reviewer exit (clean) and merges.
	daemon.monitorWorkers()

	// Verify: child worker removed from map.
	if _, exists := daemon.workers[child.ID]; exists {
		t.Error("child worker still in map after completion")
	}

	// Verify: child-work.txt is now on the parent branch.
	parentBranch := fmt.Sprintf("dispatch/plan-%s", parent.ID)
	cmd := exec.Command("git", "show", parentBranch+":child-work.txt")
	cmd.Dir = repoDir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("child-work.txt not found on parent branch: %v", err)
	}
	if string(out) != "child output" {
		t.Errorf("child-work.txt content = %q, want %q", string(out), "child output")
	}

	// Verify: child branch was deleted (clean merge).
	childBranch := fmt.Sprintf("dispatch/%s", child.ID)
	if BranchExists(repoDir, childBranch) {
		t.Error("child branch should be deleted after clean merge")
	}
}

// TestGP_DisabledNoGpBin verifies that when GPEnabled is false, gpBin stays empty.
func TestGP_DisabledNoGpBin(t *testing.T) {
	d := openTestDB(t)
	spawner := &MockSpawner{}
	daemon := New(d, Config{
		Repos:     make(map[string]config.RepoConfig),
		GPEnabled: false,
	}, spawner)

	if daemon.gpBin != "" {
		t.Errorf("gpBin = %q, want empty when GPEnabled is false", daemon.gpBin)
	}
}

// TestGP_EnabledBinaryMissingGpBinEmpty verifies that when GPEnabled is true but gp is not
// in PATH, the daemon starts without error and gpBin remains empty.
func TestGP_EnabledBinaryMissingGpBinEmpty(t *testing.T) {
	// Restrict PATH to a temp dir with no gp binary.
	emptyDir := t.TempDir()
	t.Setenv("PATH", emptyDir)

	d := openTestDB(t)
	spawner := &MockSpawner{}
	daemon := New(d, Config{
		Repos:     make(map[string]config.RepoConfig),
		GPEnabled: true,
	}, spawner)

	if daemon.gpBin != "" {
		t.Errorf("gpBin = %q, want empty when gp is not in PATH", daemon.gpBin)
	}
}

// TestGP_EnabledBinaryFoundGpBinSet verifies that when GPEnabled is true and a gp binary
// exists in PATH, gpBin is set to the resolved path.
func TestGP_EnabledBinaryFoundGpBinSet(t *testing.T) {
	// Create a fake gp binary in a temp dir.
	binDir := t.TempDir()
	gpScript := filepath.Join(binDir, "gp")
	if err := os.WriteFile(gpScript, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	d := openTestDB(t)
	spawner := &MockSpawner{}
	daemon := New(d, Config{
		Repos:     make(map[string]config.RepoConfig),
		GPEnabled: true,
	}, spawner)

	if daemon.gpBin == "" {
		t.Error("gpBin should be set when gp is in PATH")
	}
}

// TestGP_SyncChildCallsGpBinary verifies that gpSyncChild executes the gp binary with
// "sync-child <taskID>" arguments when gpBin is set.
func TestGP_SyncChildCallsGpBinary(t *testing.T) {
	// Create a mock gp binary that records its arguments to a file.
	binDir := t.TempDir()
	argsFile := filepath.Join(t.TempDir(), "gp-args.txt")
	gpScript := filepath.Join(binDir, "gp")
	scriptContent := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$*\" > %s\n", argsFile)
	if err := os.WriteFile(gpScript, []byte(scriptContent), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)

	d := openTestDB(t)
	spawner := &MockSpawner{}
	daemon := New(d, Config{
		Repos:     make(map[string]config.RepoConfig),
		GPEnabled: true,
	}, spawner)

	if daemon.gpBin == "" {
		t.Fatal("gpBin not set — mock gp binary not found in PATH")
	}

	daemon.gpSyncChild("test-task-id")

	// Wait briefly for the goroutine to complete.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(argsFile); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("args file not written: %v", err)
	}
	got := string(data)
	want := "sync-child test-task-id"
	if got != want {
		t.Errorf("gp called with args %q, want %q", got, want)
	}
}

// TestGP_SyncChildNoopWhenDisabled verifies that gpSyncChild is a no-op when gpBin is empty.
func TestGP_SyncChildNoopWhenDisabled(t *testing.T) {
	d := openTestDB(t)
	spawner := &MockSpawner{}
	daemon := New(d, Config{
		Repos:     make(map[string]config.RepoConfig),
		GPEnabled: false,
	}, spawner)

	// Should not panic and should not call any binary.
	daemon.gpSyncChild("test-task-id")
}
