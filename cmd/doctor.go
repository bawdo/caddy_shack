package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

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
	run := withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
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
	})
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose CaddyShack prerequisites; --fix to remediate",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flags before constructing the App so invalid
			// arguments (exit 2) win over a construction failure.
			if yes && !applyFix {
				return fmt.Errorf("%w: --yes requires --fix", app.ErrInvalidArgs)
			}
			if asJSON && applyFix {
				return fmt.Errorf("%w: --json cannot be combined with --fix", app.ErrInvalidArgs)
			}
			return run(cmd, args)
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

// renderDoctorJSON writes the report as indented JSON.
func renderDoctorJSON(w io.Writer, rep app.DoctorReport) {
	out, _ := json.MarshalIndent(rep, "", "  ")
	_, _ = fmt.Fprintln(w, string(out))
}

// writeCheckLine prints one check's [TAG] line, its detail lines, and the
// fix label (if any). Used by both renderDoctorHuman and the --fix loop so
// the two paths agree on what a check looks like on screen.
func writeCheckLine(w io.Writer, c app.CheckResult) {
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

func renderDoctorHuman(w io.Writer, rep app.DoctorReport) {
	for _, c := range rep.Checks {
		writeCheckLine(w, c)
	}
	_, _ = fmt.Fprintln(w)

	if rep.Interrupted {
		_, _ = fmt.Fprintf(w, "doctor interrupted after %d check(s); results are incomplete.\n", len(rep.Checks))
		return
	}
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

func runFixLoop(cmd *cobra.Command, a *app.App, rep app.DoctorReport, yes bool) error {
	out := cmd.OutOrStdout()
	in := cmd.InOrStdin()
	reader := bufio.NewReader(in)

	prompt := func(label string) bool {
		if yes {
			return true
		}
		_, _ = fmt.Fprintf(out, "Apply fix? (%s) [y/N]: ", label)
		line, err := reader.ReadString('\n')
		if err != nil && line == "" {
			return false
		}
		ans := strings.ToLower(strings.TrimSpace(line))
		return ans == "y" || ans == "yes"
	}

	var (
		applied, skipped, failed int
		// unfixed counts FAIL checks no fix was applied to (none offered,
		// or declined). They still fail, so the command must too.
		unfixed  int
		firstErr error
	)
	for _, r := range rep.Checks {
		writeCheckLine(out, r)
		if r.Fix == nil {
			if r.Status == app.SeverityFail {
				unfixed++
			}
			continue
		}
		if r.Status != app.SeverityFail && r.Status != app.SeverityWarn {
			continue
		}

		if yes && r.Fix.RequiresSudo {
			if err := sudoNonInteractiveCheck(cmd.Context()); err != nil {
				_, _ = fmt.Fprintf(out, "[FIX-FAILED] %s (sudo password required, re-run interactively)\n", r.Name)
				failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("fix %s: %w", r.Name, err)
				}
				continue
			}
		}

		outcome, err := a.ApplyFix(cmd.Context(), r, prompt)
		switch outcome {
		case app.FixApplied:
			_, _ = fmt.Fprintf(out, "[FIXED] %s\n", r.Name)
			applied++
		case app.FixSkipped:
			_, _ = fmt.Fprintf(out, "[--] %s skipped\n", r.Name)
			skipped++
			if r.Status == app.SeverityFail {
				unfixed++
			}
		case app.FixFailed:
			_, _ = fmt.Fprintf(out, "[FIX-FAILED] %s: %v\n", r.Name, err)
			failed++
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	_, _ = fmt.Fprintf(out, "\n%d fixed, %d skipped, %d failed.\n", applied, skipped, failed)
	if firstErr == nil && unfixed > 0 {
		return fmt.Errorf("doctor: %d failing check(s) not fixed", unfixed)
	}
	return firstErr
}

// sudoNonInteractiveCheck succeeds only if `sudo -n true` succeeds (i.e.
// the user has a fresh credential cache and no password prompt is needed).
// Used by --yes mode to fail fast before invoking a sudo-requiring fix.
func sudoNonInteractiveCheck(ctx context.Context) error {
	return execCommandContext(ctx, "sudo", "-n", "true").Run()
}

// runnable is the smallest interface execCommandContext returns so tests
// can stub it with their own no-op or error-returning implementation.
type runnable interface{ Run() error }

// execCommandContext is a var so tests can replace it. The production
// implementation returns *exec.Cmd, which already satisfies runnable.
var execCommandContext = func(ctx context.Context, name string, args ...string) runnable {
	return exec.CommandContext(ctx, name, args...)
}
