package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start the local Caddy container",
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			return a.Install(cmd.Context())
		}),
	}
}
