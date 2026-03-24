package commands

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/spf13/cobra"
)

// NewInitCmd returns the cobra command for initializing a repo in dispatch config.
func NewInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init <path>",
		Short: "Register a git repository with dispatch",
		Long:  "Register a git repository with dispatch by adding it to ~/.dispatch/config.toml.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Resolve path to absolute.
			repoPath, err := filepath.Abs(args[0])
			if err != nil {
				return fmt.Errorf("resolving path: %w", err)
			}

			// Verify it is a git repo.
			gitDir := filepath.Join(repoPath, ".git")
			info, err := os.Stat(gitDir)
			if err != nil {
				return fmt.Errorf("%s is not a git repository (no .git directory)", repoPath)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s is not a git repository (.git is not a directory)", repoPath)
			}

			// Check if repo already exists in config.
			cfgPath := config.DefaultConfigPath()
			if cfg, err := config.LoadConfig(cfgPath); err == nil {
				for _, r := range cfg.Repos {
					if r.Path == repoPath {
						fmt.Fprintf(os.Stderr, "Repository %s is already registered in %s\n", repoPath, cfgPath)
						return nil
					}
				}
			}
			// If LoadConfig errors (file doesn't exist, etc.), that's fine — we'll create it.

			// Prompt for max_workers.
			maxWorkers := config.DefaultMaxWorkers
			fmt.Fprintf(os.Stderr, "Max workers [%d]: ", config.DefaultMaxWorkers)
			reader := bufio.NewReader(os.Stdin)
			line, _ := reader.ReadString('\n')
			line = strings.TrimSpace(line)
			if line != "" {
				n, err := strconv.Atoi(line)
				if err != nil || n < 1 {
					return fmt.Errorf("invalid max_workers value: %q (must be a positive integer)", line)
				}
				maxWorkers = n
			}

			// Write config entry.
			repo := config.RepoConfig{
				Path:       repoPath,
				MaxWorkers: maxWorkers,
			}
			if err := config.SaveRepoEntry(cfgPath, repo); err != nil {
				return fmt.Errorf("saving config: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Registered %s (max_workers=%d) in %s\n", repoPath, maxWorkers, cfgPath)
			return nil
		},
	}

	return cmd
}
