package main

import (
	"fmt"
	"os"
	"path/filepath"

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

func main() {
	rootCmd := &cobra.Command{
		Use:   "dt",
		Short: "dispatch task tracker",
		SilenceUsage: true,
	}

	rootCmd.PersistentFlags().String("db", defaultDBPath(), "path to SQLite database")
	rootCmd.PersistentFlags().Bool("json", false, "output as JSON")

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
