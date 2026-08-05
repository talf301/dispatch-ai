package commands

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dispatch-ai/dispatch/internal/manager"
	"github.com/dispatch-ai/dispatch/internal/mux"
	"github.com/spf13/cobra"
)

// NewManagerCmd opens or recovers the one human-facing manager session.
func NewManagerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "manager",
		Short: "Open the Dispatch manager session",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			d := openDB(cmd)
			defer d.Close()
			m := manager.New(d, mux.Herdr{})
			cwd, err := os.Getwd()
			if err != nil {
				exitError(cmd, err)
			}
			if err := m.Start(cwd); err != nil {
				exitError(cmd, err)
			}
			fmt.Println("Dispatch manager ready")
			ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()
			if err := m.Run(ctx); err != nil && err != context.Canceled {
				exitError(cmd, err)
			}
		},
	}
}
