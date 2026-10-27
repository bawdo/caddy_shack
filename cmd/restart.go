package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Stop then start the Caddy container",
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			return a.Restart(cmd.Context())
		}),
	}
}
