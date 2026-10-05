package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

// Execute runs the CLI. The returned int is the process exit code.
func Execute() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err := newRootCmd().ExecuteContext(ctx)
	return classifyError(err)
}

// classifyError maps an error to documented exit codes. Delegates to
// app.ExitCode so the sentinel-to-code mapping has a single source of truth.
func classifyError(err error) int {
	return app.ExitCode(err)
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
