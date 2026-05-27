package cmd

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Report container state, admin API reachability, and backend count",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, runErr := a.Status(cmd.Context())
			w := cmd.OutOrStdout()
			if asJSON {
				out, _ := json.MarshalIndent(rep, "", "  ")
				_, _ = fmt.Fprintln(w, string(out))
			} else {
				_, _ = fmt.Fprintf(w, "container:       %s\n", rep.ContainerState)
				_, _ = fmt.Fprintf(w, "admin reachable: %t\n", rep.AdminReachable)
				_, _ = fmt.Fprintf(w, "backends:        %d\n", rep.BackendCount)
				_, _ = fmt.Fprintf(w, "healthy:         %t\n", rep.Healthy)
			}
			if runErr != nil && !errors.Is(runErr, app.ErrUnhealthy) {
				return runErr
			}
			return runErr // ErrUnhealthy -> generic exit 1 via app.ExitCode
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}
