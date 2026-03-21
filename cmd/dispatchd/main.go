// cmd/dispatchd/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/daemon"
	"github.com/dispatch-ai/dispatch/internal/db"
	"github.com/spf13/cobra"
)

func defaultDBPath() string {
	if v := os.Getenv("DISPATCH_DB"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "dispatch.db"
	}
	return filepath.Join(home, ".dispatch", "dispatch.db")
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

var rootCmd = &cobra.Command{
	Use:   "dispatchd",
	Short: "dispatch orchestration daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		maxWorkers, _ := cmd.Flags().GetInt("max-workers")
		baseBranch, _ := cmd.Flags().GetString("base-branch")
		repoPath, _ := cmd.Flags().GetString("repo")
		pollInterval, _ := cmd.Flags().GetDuration("poll-interval")

		database, err := db.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()

		home, _ := os.UserHomeDir()
		cfg := daemon.Config{
			DBPath:       dbPath,
			MaxWorkers:   maxWorkers,
			BaseBranch:   baseBranch,
			RepoPath:     repoPath,
			PollInterval: pollInterval,
			WorktreeBase: filepath.Join(home, ".dispatch", "worktrees"),
		}

		spawner := &daemon.ClaudeSpawner{
			ClaudeBin:    "claude",
			SystemPrompt: "", // TODO: load worker.md in Phase 3
			OutputLines:  100,
		}

		d := daemon.New(database, cfg, spawner)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			sig := <-sigCh
			fmt.Fprintf(os.Stderr, "\nreceived %s, shutting down...\n", sig)
			cancel()
		}()

		return d.Run(ctx)
	},
}

func init() {
	rootCmd.Flags().String("db", defaultDBPath(), "path to SQLite database")
	rootCmd.Flags().Int("max-workers", envIntOrDefault("DISPATCH_MAX_WORKERS", 4), "max concurrent workers")
	rootCmd.Flags().String("base-branch", envOrDefault("DISPATCH_BASE_BRANCH", ""), "base branch for worktrees (default: auto-detect)")
	rootCmd.Flags().String("repo", envOrDefault("DISPATCH_REPO", "."), "path to git repository")
	rootCmd.Flags().Duration("poll-interval", envDurationOrDefault("DISPATCH_POLL_INTERVAL", 5*time.Second), "poll interval")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
