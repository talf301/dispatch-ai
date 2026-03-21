package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dispatch-ai/dispatch/cmd/dt/commands"
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

var rootCmd = &cobra.Command{
	Use:          "dt",
	Short:        "dispatch task tracker",
	SilenceUsage: true,
}

func init() {
	rootCmd.PersistentFlags().String("db", defaultDBPath(), "path to SQLite database")
	rootCmd.PersistentFlags().Bool("json", false, "output as JSON")

	rootCmd.AddCommand(commands.NewAddCmd())
	rootCmd.AddCommand(commands.NewEditCmd())
	rootCmd.AddCommand(commands.NewShowCmd())
	rootCmd.AddCommand(commands.NewDepCmd())
	rootCmd.AddCommand(commands.NewUndepCmd())
	rootCmd.AddCommand(commands.NewClaimCmd())
	rootCmd.AddCommand(commands.NewReleaseCmd())
	rootCmd.AddCommand(commands.NewDoneCmd())
	rootCmd.AddCommand(commands.NewBlockCmd())
	rootCmd.AddCommand(commands.NewReopenCmd())
	rootCmd.AddCommand(commands.NewNoteCmd())
	rootCmd.AddCommand(commands.NewReadyCmd())
	rootCmd.AddCommand(commands.NewListCmd())
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
