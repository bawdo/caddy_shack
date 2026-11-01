package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the Caddy container",
		Args:  noArgs,
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			return a.Stop()
		}),
	}
}
