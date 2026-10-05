package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start the local Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Install(cmd.Context())
		},
	}
}
