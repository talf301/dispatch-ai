package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadReposFiltersUnavailableConfigAndFallsBack(t *testing.T) {
	tmp := t.TempDir()
	available := filepath.Join(tmp, "available")
	if err := os.MkdirAll(filepath.Join(available, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	unavailable := filepath.Join(tmp, "gone")
	configPath := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(configPath, []byte("[[repo]]\npath = \""+available+"\"\n\n[[repo]]\npath = \""+unavailable+"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	repos, err := loadRepos(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[available].Path != available {
		t.Fatalf("repos = %#v, want only %q", repos, available)
	}

	if err := os.RemoveAll(available); err != nil {
		t.Fatal(err)
	}
	fallback := filepath.Join(tmp, "fallback")
	repos, err = loadRepos(configPath, fallback)
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[fallback].Path != fallback {
		t.Fatalf("repos = %#v, want fallback %q", repos, fallback)
	}
}
