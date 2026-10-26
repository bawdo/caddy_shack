package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

// withApp adapts a command body that needs an App into a cobra RunE. It
// constructs the App first and returns any construction error unchanged.
func withApp(run func(cmd *cobra.Command, args []string, a *app.App) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a, err := app.New()
		if err != nil {
			return err
		}
		return run(cmd, args, a)
	}
}
