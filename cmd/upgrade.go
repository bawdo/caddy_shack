package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newUpgradeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade",
		Short: "Re-pull caddy:2 and recreate the container, preserving config",
		Long: `Re-pulls the caddy:2 image and, if it differs from the running image,
recreates the container against it. Both named volumes are preserved, so
certs survive, and registered backends are restored from state. No-op when
already up to date.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, err := a.Upgrade(cmd.Context())
			if err != nil {
				return err
			}
			renderUpgrade(cmd.OutOrStdout(), rep)
			return nil
		},
	}
}

// renderUpgrade writes the human-readable outcome of an upgrade to w.
func renderUpgrade(w io.Writer, rep app.UpgradeReport) {
	switch {
	case rep.AlreadyCurrent && rep.Version != "":
		_, _ = fmt.Fprintf(w, "already up to date (%s)\n", rep.Version)
	case rep.AlreadyCurrent:
		_, _ = fmt.Fprintln(w, "already up to date")
	case rep.Version != "":
		_, _ = fmt.Fprintf(w, "upgraded to %s\n", rep.Version)
	default:
		_, _ = fmt.Fprintln(w, "upgrade complete; run caddy_shack status to confirm version")
	}
}
