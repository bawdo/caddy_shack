package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

// newApp builds the App every command runs against. It is a variable so the
// cmd tests can drive the real command tree against an App wired to fakes
// (a docker.Fake, an httptest Caddy admin API, temporary state paths).
// Production never reassigns it.
var newApp = app.New

// withApp adapts a command body that needs an App into a cobra RunE. It
// constructs the App first and returns any construction error unchanged.
func withApp(run func(cmd *cobra.Command, args []string, a *app.App) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		a, err := newApp()
		if err != nil {
			return err
		}
		return run(cmd, args, a)
	}
}
