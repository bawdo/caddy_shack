package cmd

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

// Execute runs the CLI. The returned int is the process exit code.
func Execute() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	return app.ExitCode(execute(ctx, newRootCmd()))
}

// execute runs root and reports any error cobra raised before a command body
// started as a usage error, so it exits 2 as the help promises. Cobra checks
// the command name, flags, positional arguments and required flags itself and
// returns plain errors with no sentinel, so "no RunE started" is the one
// signal that covers all of them. Cobra has already printed its own message
// by the time it returns, so only the exit code changes.
//
// This holds while no command has a PreRun hook: an error from a PreRunE or
// PersistentPreRunE would also read as a usage error.
// TestNoCommandHasPreRunHooks guards that.
func execute(ctx context.Context, root *cobra.Command) error {
	// Cobra adds its completion commands, which use RunE, only inside
	// ExecuteContext. Adding them first (cobra skips the step once they
	// exist) puts them in the tree markRunStart walks. A hook cannot do this
	// job: PersistentPreRun runs before cobra checks required flags.
	root.InitDefaultCompletionCmd()
	started := false
	markRunStart(root, &started)
	err := root.ExecuteContext(ctx)
	if err != nil && !started && !errors.Is(err, app.ErrInvalidArgs) {
		return fmt.Errorf("%w: %w", app.ErrInvalidArgs, err)
	}
	return err
}

// markRunStart wraps every RunE in the tree so it sets *started on entry.
func markRunStart(cmd *cobra.Command, started *bool) {
	if run := cmd.RunE; run != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			*started = true
			return run(c, args)
		}
	}
	for _, sub := range cmd.Commands() {
		markRunStart(sub, started)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "caddy_shack",
		Short: "Manage a local Caddy instance for HTTPS .test URLs",
		Long: `caddy_shack manages a locally-running Caddy container so backend services
can register themselves and get a stable HTTPS URL under the .test TLD.

Designed to be called from project startup and teardown scripts:
  caddy_shack register myapp --port 3000
  caddy_shack deregister myapp

Exit codes (stable, script-friendly):
  0  success
  1  generic command failure (e.g. Caddy rejected the config; status unhealthy)
  2  invalid arguments
  3  domain conflict (another backend already owns this domain)
  4  docker daemon unreachable
  5  caddy container not running
  6  caddy not installed (no container exists)
  7  state file corrupt - manual remediation required
  8  state lock held by another caddy_shack process
  9  status only: Caddy is up but its live config does not match the
     registry (e.g. it restarted and lost its routes); run 'caddy_shack sync'
  10  LAN mode not enabled
  11  certificate issuance failed (ACME order, DNS validation, or rate limit)

See docs/superpowers/specs/2026-05-27-mvp-vertical-slice-design.md for the
full design.`,
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd())
	root.AddCommand(newInstallCmd())
	root.AddCommand(newStartCmd())
	root.AddCommand(newStopCmd())
	root.AddCommand(newRestartCmd())
	root.AddCommand(newUpgradeCmd())
	root.AddCommand(newRegisterCmd())
	root.AddCommand(newDeregisterCmd())
	root.AddCommand(newListCmd())
	root.AddCommand(newStatusCmd())
	root.AddCommand(newSyncCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newCACmd())
	root.AddCommand(newLANCmd())
	root.AddCommand(newCertCmd())
	applyHelpBareword(root)
	return root
}

func applyHelpBareword(cmd *cobra.Command) {
	orig := cmd.RunE
	if orig != nil {
		cmd.RunE = func(c *cobra.Command, args []string) error {
			if len(args) > 0 && args[0] == "help" {
				return c.Help()
			}
			return orig(c, args)
		}
	}
	for _, sub := range cmd.Commands() {
		applyHelpBareword(sub)
	}
}
