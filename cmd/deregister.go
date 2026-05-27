package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDeregisterCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deregister <name>",
		Short: "Deregister a previously registered backend",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			if err := a.Deregister(cmd.Context(), args[0]); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "deregistered:", args[0])
			return nil
		},
	}
}
