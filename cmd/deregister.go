package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newDeregisterCmd() *cobra.Command {
	var cascade bool
	c := &cobra.Command{
		Use:   "deregister <name>",
		Short: "Deregister a previously registered backend",
		Long: `Deregister a previously registered backend.

By default only the named backend is removed; any subdomain backends
registered under it (e.g. bar.foo when removing foo) are left in place and
reported on stderr. Pass --cascade to also remove every subdomain backend
under the named one.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			res, err := a.Deregister(cmd.Context(), args[0], cascade)
			if err != nil {
				return err
			}
			renderDeregister(cmd.OutOrStdout(), cmd.ErrOrStderr(), args[0], res)
			return nil
		},
	}
	c.Flags().BoolVar(&cascade, "cascade", false,
		"Also remove subdomain backends registered under <name>")
	return c
}

// renderDeregister writes the human-facing outcome of a deregister: success
// lines (removed backend and any cascaded children) to out, and warnings or
// not-found notices to errOut. Extracted from RunE so the messaging can be
// tested without spinning up a real App.
func renderDeregister(out, errOut io.Writer, name string, res app.DeregisterResult) {
	for _, child := range res.Cascaded {
		_, _ = fmt.Fprintln(out, "deregistered:", child)
	}
	if res.Removed {
		_, _ = fmt.Fprintln(out, "deregistered:", name)
		if len(res.ChildrenLeft) > 0 {
			_, _ = fmt.Fprintf(errOut,
				"%d subdomain backend(s) still registered under %s (use --cascade to remove)\n",
				len(res.ChildrenLeft), res.ParentDomain)
		}
	} else if len(res.Cascaded) == 0 {
		_, _ = fmt.Fprintln(errOut, "no such backend:", name)
	}
}
