package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the existing Caddy container",
		Args:  noArgs,
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			return a.Start(cmd.Context())
		}),
	}
}
