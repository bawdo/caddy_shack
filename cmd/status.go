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
	if rep.CertCount > 0 {
		_, _ = fmt.Fprintf(w, "certificates:    %d\n", rep.CertCount)
		if rep.SoonestCertExpiryDays != nil {
			_, _ = fmt.Fprintf(w, "soonest expiry:  %s\n", formatExpiry(*rep.SoonestCertExpiryDays))
		}
	}
	_, _ = fmt.Fprintf(w, "healthy:         %t\n", rep.Healthy)
	renderStatusLAN(w, rep.LAN)
}

// renderStatusLAN writes the informational LAN block. It is purely
// descriptive and never feeds into health or the exit code.
func renderStatusLAN(w io.Writer, l *app.LANStatus) {
	if l == nil {
		return
	}
	if !l.Enabled {
		_, _ = fmt.Fprintln(w, "LAN:        disabled")
		return
	}
	_, _ = fmt.Fprintf(w, "LAN:        enabled (%s) %s, suffix %s\n", l.Mode, l.IP, l.Suffix)
	if !l.IPPresent {
		_, _ = fmt.Fprintf(w, "            %s is not on any interface\n", l.IP)
	}
	_, _ = fmt.Fprintf(w, "forwarder:  %s\n", l.ForwarderState)
	if l.DiscoveredIP != "" {
		_, _ = fmt.Fprintf(w, "drift:      interface now has %s, run caddy_shack restart\n", l.DiscoveredIP)
	} else {
		_, _ = fmt.Fprintln(w, "drift:      none")
	}
	_, _ = fmt.Fprintf(w, "backends:   %d with LAN names\n", l.LANBackends)
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
