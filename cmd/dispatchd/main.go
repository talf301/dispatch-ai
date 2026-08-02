// cmd/dispatchd/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/dispatch-ai/dispatch/internal/config"
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

func envDurationOrDefault(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

const (
	defaultCodexWorkerModel           = "gpt-5.6-luna"
	defaultCodexWorkerEscalationModel = "gpt-5.6-terra"
	defaultWorkerEscalateAfter        = 2
)

var rootCmd = &cobra.Command{
	Use:   "dispatchd",
	Short: "dispatch orchestration daemon",
	RunE: func(cmd *cobra.Command, args []string) error {
		dbPath, _ := cmd.Flags().GetString("db")
		baseBranch, _ := cmd.Flags().GetString("base-branch")
		repoPath, _ := cmd.Flags().GetString("repo")
		pollInterval, _ := cmd.Flags().GetDuration("poll-interval")
		workerPromptPath, _ := cmd.Flags().GetString("worker-prompt")
		reviewerPromptPath, _ := cmd.Flags().GetString("reviewer-prompt")
		workerAgent, _ := cmd.Flags().GetString("worker-agent")
		reviewerAgent, _ := cmd.Flags().GetString("reviewer-agent")
		workerModel, _ := cmd.Flags().GetString("worker-model")
		workerEscalationModel, _ := cmd.Flags().GetString("worker-escalation-model")
		workerEscalateAfter, _ := cmd.Flags().GetInt("worker-escalate-after")
		if workerAgent == "codex" {
			if workerModel == "" {
				workerModel = defaultCodexWorkerModel
			}
			if workerEscalationModel == "" {
				workerEscalationModel = defaultCodexWorkerEscalationModel
			}
		}
		gpEnabled, _ := cmd.Flags().GetBool("gp")
		managerEnabled, _ := cmd.Flags().GetBool("manager")

		if workerPromptPath == "" || reviewerPromptPath == "" {
			return fmt.Errorf("--worker-prompt and --reviewer-prompt are required")
		}

		workerPrompt, err := os.ReadFile(workerPromptPath)
		if err != nil {
			return fmt.Errorf("read worker prompt: %w", err)
		}
		reviewerPrompt, err := os.ReadFile(reviewerPromptPath)
		if err != nil {
			return fmt.Errorf("read reviewer prompt: %w", err)
		}

		database, err := db.Open(dbPath)
		if err != nil {
			return fmt.Errorf("open database: %w", err)
		}
		defer database.Close()

		home, _ := os.UserHomeDir()

		// Build repos map: load config.toml if present, fall back to --repo.
		repos := make(map[string]config.RepoConfig)
		configPath := config.DefaultConfigPath()
		if cfg, err := config.LoadConfig(configPath); err == nil && len(cfg.Repos) > 0 {
			for _, r := range cfg.Repos {
				repos[r.Path] = r
			}
		} else if repoPath != "" {
			// Single-repo mode via --repo flag.
			absRepo, err := filepath.Abs(repoPath)
			if err != nil {
				absRepo = repoPath
			}
			repos[absRepo] = config.RepoConfig{
				Path:       absRepo,
				MaxWorkers: config.DefaultMaxWorkers,
			}
		} else {
			return fmt.Errorf("no repos configured: create %s or pass --repo", configPath)
		}

		cfg := daemon.Config{
			DBPath:                dbPath,
			Repos:                 repos,
			BaseBranch:            baseBranch,
			PollInterval:          pollInterval,
			WorktreeBase:          filepath.Join(home, ".dispatch", "worktrees"),
			SessionDir:            filepath.Join(home, ".dispatch", "sessions"),
			GPEnabled:             gpEnabled,
			Mux:                   daemon.MuxIfAvailable(log.New(os.Stderr, "[dispatchd] ", log.LstdFlags)),
			ReviewerAgent:         reviewerAgent,
			WorkerModel:           workerModel,
			WorkerEscalationModel: workerEscalationModel,
			WorkerEscalateAfter:   workerEscalateAfter,
			Manager:               managerEnabled,
		}

		newSpawner := func(agent string) *daemon.CLISpawner {
			return &daemon.CLISpawner{
				Agent:          agent,
				WorkerPrompt:   string(workerPrompt),
				ReviewerPrompt: string(reviewerPrompt),
				OutputLines:    100,
				SessionDir:     filepath.Join(home, ".dispatch", "sessions"),
				UsageDB:        database,
			}
		}
		spawner := &daemon.RoleSpawner{
			Worker:   newSpawner(workerAgent),
			Reviewer: newSpawner(reviewerAgent),
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
	rootCmd.Flags().String("base-branch", envOrDefault("DISPATCH_BASE_BRANCH", ""), "base branch for worktrees (default: auto-detect)")
	rootCmd.Flags().String("repo", envOrDefault("DISPATCH_REPO", ""), "path to git repository (single-repo mode)")
	rootCmd.Flags().Duration("poll-interval", envDurationOrDefault("DISPATCH_POLL_INTERVAL", 5*time.Second), "poll interval")
	rootCmd.Flags().String("worker-prompt", envOrDefault("DISPATCH_WORKER_PROMPT", ""), "path to worker.md prompt file (required)")
	rootCmd.Flags().String("reviewer-prompt", envOrDefault("DISPATCH_REVIEWER_PROMPT", ""), "path to reviewer.md prompt file (required)")
	rootCmd.Flags().String("worker-agent", envOrDefault("DISPATCH_WORKER_AGENT", "claude"), "agent CLI for workers: claude or codex")
	rootCmd.Flags().String("reviewer-agent", envOrDefault("DISPATCH_REVIEWER_AGENT", "claude"), "agent CLI for the review gate: claude or codex")
	rootCmd.Flags().String("worker-model", os.Getenv("DISPATCH_WORKER_MODEL"), "explicit model for workers (optional)")
	rootCmd.Flags().String("worker-escalation-model", os.Getenv("DISPATCH_WORKER_ESCALATION_MODEL"), "model for workers after repeated review rejection (optional)")
	rootCmd.Flags().Int("worker-escalate-after", envIntOrDefault("DISPATCH_WORKER_ESCALATE_AFTER", defaultWorkerEscalateAfter), "rejected review rounds before worker model escalation")
	rootCmd.Flags().Bool("gp", os.Getenv("DISPATCH_GP") == "1", "enable GraphPilot integration (env: DISPATCH_GP=1)")
	rootCmd.Flags().Bool("manager", os.Getenv("DISPATCH_MANAGER") == "1", "run the human-facing manager session")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
