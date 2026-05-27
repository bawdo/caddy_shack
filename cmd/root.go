package cmd

import (
	"context"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

// Execute runs the CLI. The returned int is the process exit code.
func Execute() int {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err := newRootCmd().ExecuteContext(ctx)
	return classifyError(err)
}

// classifyError maps an error to documented exit codes. Empty for now;
// add sentinel errors as the project grows.
func classifyError(err error) int {
	if err == nil {
		return 0
	}
	return 1
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "caddy_shack",
		Short:         "(add a short description)",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	root.AddCommand(newVersionCmd())
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
