package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the existing Caddy container",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Start()
		},
	}
}
