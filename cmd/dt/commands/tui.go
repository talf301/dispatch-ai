package commands

import (
	"os"

	"github.com/dispatch-ai/dispatch/internal/config"
	"github.com/dispatch-ai/dispatch/internal/mux"
	"github.com/dispatch-ai/dispatch/internal/tui"
	"github.com/spf13/cobra"
)

// NewTuiCmd runs the board.
func NewTuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "The board: everything in flight, one surface",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			d := openDB(cmd)
			defer d.Close()

			dtBin, err := os.Executable()
			if err != nil {
				dtBin = "dt"
			}
			var repos []string
			if cfg, err := config.LoadConfig(config.DefaultConfigPath()); err == nil {
				for _, repo := range cfg.Repos {
					repos = append(repos, repo.Path)
				}
			}
			cwd, _ := os.Getwd()
			currentRepo, _ := gitToplevel(cwd)
			if err := tui.Run(d, mux.Herdr{}, dtBin, repos, currentRepo); err != nil {
				exitError(cmd, err)
			}
		},
	}
}
