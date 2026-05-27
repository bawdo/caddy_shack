package cmd

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDoctorCmd() *cobra.Command {
	var (
		asJSON     bool
		applyFix   bool
		yes        bool
		noBackends bool
	)
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose CaddyShack prerequisites; --fix to remediate",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if yes && !applyFix {
				return fmt.Errorf("%w: --yes requires --fix", app.ErrInvalidArgs)
			}
			if asJSON && applyFix {
				return fmt.Errorf("%w: --json cannot be combined with --fix", app.ErrInvalidArgs)
			}
			a, err := app.New()
			if err != nil {
				return err
			}
			rep, err := a.Doctor(cmd.Context(), app.DoctorOptions{SkipBackends: noBackends})
			if err != nil {
				// Cancelled mid-run; render what we have and propagate.
				renderDoctorReport(cmd.OutOrStdout(), rep, asJSON)
				return err
			}
			if applyFix {
				return runFixLoop(cmd, a, rep, yes)
			}
			renderDoctorReport(cmd.OutOrStdout(), rep, asJSON)
			if rep.Summary.Fail > 0 {
				return fmt.Errorf("doctor: %d failing check(s)", rep.Summary.Fail)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	c.Flags().BoolVar(&applyFix, "fix", false, "Apply available fixes (interactive)")
	c.Flags().BoolVar(&yes, "yes", false, "Apply every fix without prompting (requires --fix)")
	c.Flags().BoolVar(&noBackends, "no-backends", false, "Skip backend reachability probes")
	return c
}

// renderDoctorReport dispatches to the human or JSON renderer.
func renderDoctorReport(w io.Writer, rep app.DoctorReport, asJSON bool) {
	if asJSON {
		renderDoctorJSON(w, rep)
		return
	}
	renderDoctorHuman(w, rep)
}

// renderDoctorJSON is implemented in Task 16.
func renderDoctorJSON(w io.Writer, rep app.DoctorReport) {
	out, _ := json.MarshalIndent(rep, "", "  ")
	_, _ = fmt.Fprintln(w, string(out))
}

func renderDoctorHuman(w io.Writer, rep app.DoctorReport) {
	for _, c := range rep.Checks {
		_, _ = fmt.Fprintf(w, "[%s] %-20s %s\n", severityTag(c.Status), c.Name, c.Summary)
		for _, d := range c.Detail {
			_, _ = fmt.Fprintf(w, "       %s\n", d)
		}
		if c.Fix != nil {
			sudo := ""
			if c.Fix.RequiresSudo {
				sudo = " (requires sudo)"
			}
			_, _ = fmt.Fprintf(w, "       fix: %s%s\n", c.Fix.Label, sudo)
		}
	}
	_, _ = fmt.Fprintln(w)

	issues := rep.Summary.Fail + rep.Summary.Warn
	if issues == 0 {
		_, _ = fmt.Fprintln(w, "all checks passed.")
		return
	}
	_, _ = fmt.Fprintf(w, "%d issue(s) found (%d FAIL, %d WARN). Run with --fix to remediate.\n",
		issues, rep.Summary.Fail, rep.Summary.Warn)
}

func severityTag(s app.Severity) string {
	switch s {
	case app.SeverityPass:
		return "PASS"
	case app.SeverityWarn:
		return "WARN"
	case app.SeverityFail:
		return "FAIL"
	case app.SeveritySkip:
		return "SKIP"
	default:
		return "????"
	}
}

// runFixLoop is implemented in Task 17.
func runFixLoop(cmd *cobra.Command, a *app.App, rep app.DoctorReport, yes bool) error {
	_, _ = a, rep
	_ = yes
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "fix loop not yet implemented")
	return nil
}
