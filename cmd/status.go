package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Report container health and whether Caddy's live config matches the registry",
		Long: `Report the Caddy container's state, admin API reachability, the number of
registered backends, and whether Caddy's live config (GET /config/) matches
what the registry (state.json) renders.

status only reads. It exits 0 when healthy, 9 when everything is up but the
live config has drifted from the registry (run 'caddy_shack sync'), and 1
for any other unhealthy state. See 'caddy_shack --help' for every code.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, runErr := a.Status(cmd.Context())
			renderStatus(cmd.OutOrStdout(), rep, asJSON)
			return runErr // exit code via app.ExitCode: 1 unhealthy, 9 drift
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}

// renderStatus writes the human or JSON form of the status report to w.
// Extracted from RunE so the formatting can be tested without a real App.
func renderStatus(w io.Writer, rep app.StatusReport, asJSON bool) {
	if asJSON {
		out, _ := json.MarshalIndent(rep, "", "  ")
		_, _ = fmt.Fprintln(w, string(out))
		return
	}
	_, _ = fmt.Fprintf(w, "container:       %s\n", rep.ContainerState)
	if rep.CaddyVersion != "" {
		_, _ = fmt.Fprintf(w, "version:         %s\n", rep.CaddyVersion)
	}
	if rep.RelayState != "" {
		_, _ = fmt.Fprintf(w, "relay:           %s\n", rep.RelayState)
	}
	_, _ = fmt.Fprintf(w, "admin reachable: %t\n", rep.AdminReachable)
	_, _ = fmt.Fprintf(w, "backends:        %d\n", rep.BackendCount)
	switch {
	case rep.Drift != nil:
		_, _ = fmt.Fprintf(w, "config in sync:  %t\n", rep.Drift.InSync)
		if !rep.Drift.InSync {
			for _, line := range app.DriftDetail(*rep.Drift) {
				_, _ = fmt.Fprintf(w, "  %s\n", line)
			}
			_, _ = fmt.Fprintln(w, "  run 'caddy_shack sync' to re-push the registry to Caddy")
		}
	case rep.DriftError != "":
		_, _ = fmt.Fprintf(w, "config in sync:  unknown (%s)\n", rep.DriftError)
	default:
		_, _ = fmt.Fprintln(w, "config in sync:  unknown")
	}
	_, _ = fmt.Fprintf(w, "healthy:         %t\n", rep.Healthy)
}
