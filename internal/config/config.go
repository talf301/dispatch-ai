// Package config handles parsing and writing of the dispatch config file (~/.dispatch/config.toml).
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RepoConfig holds the configuration for a single repository.
type RepoConfig struct {
	Path         string `toml:"path"`
	MaxWorkers   int    `toml:"max_workers"`
	DeliveryMode string `toml:"delivery_mode"`
}

// Config is the top-level dispatch configuration.
type Config struct {
	Repos []RepoConfig `toml:"repo"`
}

// DefaultMaxWorkers is the default number of concurrent workers per repo.
const DefaultMaxWorkers = 4

const (
	DeliveryModeNoMistakes = "no-mistakes"
	DeliveryModeDirectPR   = "direct-PR"
	DeliveryModeLocalOnly  = "local-only"
	DefaultDeliveryMode    = DeliveryModeDirectPR
)

func validDeliveryMode(mode string) bool {
	return mode == DeliveryModeNoMistakes || mode == DeliveryModeDirectPR || mode == DeliveryModeLocalOnly
}

// LoadConfig parses and validates the config file at the given path.
// It validates that paths are absolute, exist as git repos, rejects duplicates,
// and applies defaults where unset.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	seen := make(map[string]bool)
	for i := range cfg.Repos {
		r := &cfg.Repos[i]

		// Validate absolute path.
		if !filepath.IsAbs(r.Path) {
			return nil, fmt.Errorf("repo path must be absolute: %q", r.Path)
		}

		// Validate path exists and is a git repo.
		gitDir := filepath.Join(r.Path, ".git")
		info, err := os.Stat(gitDir)
		if err != nil {
			return nil, fmt.Errorf("repo path %q is not a valid git repository: %w", r.Path, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("repo path %q is not a valid git repository: .git is not a directory", r.Path)
		}

		// Reject duplicates.
		if seen[r.Path] {
			return nil, fmt.Errorf("duplicate repo path: %q", r.Path)
		}
		seen[r.Path] = true

		// Apply default MaxWorkers.
		if r.MaxWorkers == 0 {
			r.MaxWorkers = DefaultMaxWorkers
		}
		if r.DeliveryMode == "" {
			r.DeliveryMode = DefaultDeliveryMode
		} else if !validDeliveryMode(r.DeliveryMode) {
			return nil, fmt.Errorf("invalid delivery_mode %q: must be %q, %q, or %q", r.DeliveryMode, DeliveryModeNoMistakes, DeliveryModeDirectPR, DeliveryModeLocalOnly)
		}
	}

	return &cfg, nil
}

// DefaultConfigPath returns the default path for the config file.
func DefaultConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".dispatch", "config.toml")
}

// SaveRepoEntry appends a [[repo]] block to the config file at the given path.
// If the file does not exist, it is created (along with parent directories).
func SaveRepoEntry(path string, repo RepoConfig) error {
	if repo.MaxWorkers == 0 {
		repo.MaxWorkers = DefaultMaxWorkers
	}
	if repo.DeliveryMode == "" {
		repo.DeliveryMode = DefaultDeliveryMode
	}
	if !validDeliveryMode(repo.DeliveryMode) {
		return fmt.Errorf("invalid delivery_mode %q: must be %q, %q, or %q", repo.DeliveryMode, DeliveryModeNoMistakes, DeliveryModeDirectPR, DeliveryModeLocalOnly)
	}

	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening config file: %w", err)
	}
	defer f.Close()

	// Check if file has content; if so, add a leading newline for separation.
	info, _ := f.Stat()
	prefix := ""
	if info.Size() > 0 {
		prefix = "\n"
	}

	entry := fmt.Sprintf("%s[[repo]]\npath = %q\nmax_workers = %d\ndelivery_mode = %q\n", prefix, repo.Path, repo.MaxWorkers, repo.DeliveryMode)
	if _, err := f.WriteString(entry); err != nil {
		return fmt.Errorf("writing config entry: %w", err)
	}

	return nil
}
