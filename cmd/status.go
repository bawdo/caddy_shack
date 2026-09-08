package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
			renderStatus(cmd.OutOrStdout(), rep, asJSON)
			if runErr != nil && !errors.Is(runErr, app.ErrUnhealthy) {
				return runErr
			}
			return runErr // ErrUnhealthy -> generic exit 1 via app.ExitCode
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
	_, _ = fmt.Fprintf(w, "admin reachable: %t\n", rep.AdminReachable)
	_, _ = fmt.Fprintf(w, "backends:        %d\n", rep.BackendCount)
	if rep.CertCount > 0 {
		_, _ = fmt.Fprintf(w, "certificates:    %d\n", rep.CertCount)
		if rep.SoonestCertExpiryDays != nil {
			_, _ = fmt.Fprintf(w, "soonest expiry:  %s\n", formatExpiry(*rep.SoonestCertExpiryDays))
		}
	}
	_, _ = fmt.Fprintf(w, "healthy:         %t\n", rep.Healthy)
}

// formatExpiry renders a days-remaining count from app.StatusReport in plain
// language for the human status output. Negative values (an expiry already
// in the past) are spelled out as "expired N days ago" rather than a bare
// negative number, which is the one line in `status` that someone is most
// likely to be reading because something is actually wrong. Zero is the
// genuinely ambiguous case: DaysRemaining truncates toward zero, so a
// certificate that expired a few hours ago and one expiring in the next
// twenty-odd hours both compute as 0. Rather than assert either "today" or
// "expired", the zero case says so honestly.
func formatExpiry(days int) string {
	switch {
	case days > 0:
		return fmt.Sprintf("%d %s", days, pluralDays(days))
	case days < 0:
		n := -days
		return fmt.Sprintf("expired %d %s ago", n, pluralDays(n))
	default:
		return "today or in the last day (check cert list)"
	}
}

// pluralDays returns "day" for n == 1 and "days" otherwise, so formatExpiry
// never prints "1 days".
func pluralDays(n int) string {
	if n == 1 {
		return "day"
	}
	return "days"
}
