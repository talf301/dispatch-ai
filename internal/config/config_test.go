package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// helper to create a fake git repo directory
func makeGitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_Valid(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repoA")
	repoB := filepath.Join(tmp, "repoB")
	makeGitRepo(t, repoA)
	makeGitRepo(t, repoB)

	content := `[[repo]]
path = "` + repoA + `"
max_workers = 2

[[repo]]
path = "` + repoB + `"
`
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}

	if len(cfg.Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(cfg.Repos))
	}

	if cfg.Repos[0].Path != repoA || cfg.Repos[0].MaxWorkers != 2 {
		t.Errorf("repo[0] = %+v, want path=%s maxWorkers=2", cfg.Repos[0], repoA)
	}

	// Default MaxWorkers applied
	if cfg.Repos[1].Path != repoB || cfg.Repos[1].MaxWorkers != DefaultMaxWorkers {
		t.Errorf("repo[1] = %+v, want path=%s maxWorkers=%d", cfg.Repos[1], repoB, DefaultMaxWorkers)
	}
}

func TestLoadConfig_RelativePath(t *testing.T) {
	tmp := t.TempDir()
	content := `[[repo]]
path = "relative/path"
`
	cfgPath := filepath.Join(tmp, "config.toml")
	os.WriteFile(cfgPath, []byte(content), 0o644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "must be absolute") {
		t.Errorf("error = %v, want 'must be absolute'", err)
	}
}

func TestLoadConfig_NotGitRepo(t *testing.T) {
	tmp := t.TempDir()
	notRepo := filepath.Join(tmp, "notrepo")
	os.MkdirAll(notRepo, 0o755) // no .git

	content := `[[repo]]
path = "` + notRepo + `"
`
	cfgPath := filepath.Join(tmp, "config.toml")
	os.WriteFile(cfgPath, []byte(content), 0o644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
	if !strings.Contains(err.Error(), "not a valid git repository") {
		t.Errorf("error = %v, want 'not a valid git repository'", err)
	}
}

func TestLoadConfig_DuplicatePaths(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	makeGitRepo(t, repo)

	content := `[[repo]]
path = "` + repo + `"

[[repo]]
path = "` + repo + `"
`
	cfgPath := filepath.Join(tmp, "config.toml")
	os.WriteFile(cfgPath, []byte(content), 0o644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for duplicate paths")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error = %v, want 'duplicate'", err)
	}
}

func TestLoadConfig_InvalidTOML(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	os.WriteFile(cfgPath, []byte("not valid [[ toml"), 0o644)

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
	if !strings.Contains(err.Error(), "parsing config") {
		t.Errorf("error = %v, want 'parsing config'", err)
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.toml")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadConfig_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	os.WriteFile(cfgPath, []byte(""), 0o644)

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.Repos) != 0 {
		t.Errorf("expected 0 repos, got %d", len(cfg.Repos))
	}
}

func TestSaveRepoEntry_NewFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sub", "config.toml")

	err := SaveRepoEntry(cfgPath, RepoConfig{Path: "/some/repo", MaxWorkers: 3})
	if err != nil {
		t.Fatalf("SaveRepoEntry: %v", err)
	}

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `[[repo]]`) {
		t.Errorf("missing [[repo]] header in:\n%s", got)
	}
	if !strings.Contains(got, `path = "/some/repo"`) {
		t.Errorf("missing path in:\n%s", got)
	}
	if !strings.Contains(got, `max_workers = 3`) {
		t.Errorf("missing max_workers in:\n%s", got)
	}
}

func TestSaveRepoEntry_AppendsToExisting(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	// Write first entry
	SaveRepoEntry(cfgPath, RepoConfig{Path: "/repo/one", MaxWorkers: 2})
	// Write second entry
	SaveRepoEntry(cfgPath, RepoConfig{Path: "/repo/two", MaxWorkers: 5})

	data, _ := os.ReadFile(cfgPath)
	got := string(data)

	if strings.Count(got, "[[repo]]") != 2 {
		t.Errorf("expected 2 [[repo]] blocks, got:\n%s", got)
	}
	if !strings.Contains(got, `"/repo/one"`) || !strings.Contains(got, `"/repo/two"`) {
		t.Errorf("missing repo paths in:\n%s", got)
	}
}

func TestSaveRepoEntry_DefaultMaxWorkers(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	SaveRepoEntry(cfgPath, RepoConfig{Path: "/some/repo"})

	data, _ := os.ReadFile(cfgPath)
	got := string(data)
	if !strings.Contains(got, "max_workers = 4") {
		t.Errorf("expected default max_workers = 4 in:\n%s", got)
	}
}

func TestSaveRepoEntry_RoundTrip(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "myrepo")
	makeGitRepo(t, repo)

	cfgPath := filepath.Join(tmp, "config.toml")
	err := SaveRepoEntry(cfgPath, RepoConfig{Path: repo, MaxWorkers: 6})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig after SaveRepoEntry: %v", err)
	}
	if len(cfg.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(cfg.Repos))
	}
	if cfg.Repos[0].Path != repo {
		t.Errorf("path = %q, want %q", cfg.Repos[0].Path, repo)
	}
	if cfg.Repos[0].MaxWorkers != 6 {
		t.Errorf("max_workers = %d, want 6", cfg.Repos[0].MaxWorkers)
	}
}
