package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newSyncCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "sync",
		Short: "Make Caddy's live config match the registry",
		Long: `Compare Caddy's live config with what the registry (state.json) renders
and, if they differ, re-push the registry to Caddy. The registry is the
source of truth: live routes it doesn't know about are removed.

start, restart, install, register and deregister already do this, so sync
is for scripts that act on 'caddy_shack status' exiting 9 (drift) without
changing anything else. Safe to run at any time; a no-op when in sync.`,
		Args: noArgs,
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			rep, err := a.Sync(cmd.Context())
			if err != nil {
				return err
			}
			renderSync(cmd.OutOrStdout(), rep, asJSON)
			return nil
		}),
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}

// renderSync writes the human or JSON form of a sync outcome to w.
func renderSync(w io.Writer, rep app.SyncReport, asJSON bool) {
	if asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		_, _ = fmt.Fprintln(w, string(out))
		return
	}
	if !rep.Pushed {
		_, _ = fmt.Fprintln(w, "in sync: live Caddy config matches the registry")
		return
	}
	for _, line := range app.DriftDetail(rep.Before) {
		_, _ = fmt.Fprintf(w, "drift: %s\n", line)
	}
	_, _ = fmt.Fprintln(w, "synced: re-pushed the registry to Caddy")
}
