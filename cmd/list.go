package cmd

import (
	"encoding/json"
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List registered backends",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			items, err := a.List(cmd.Context())
			if err != nil {
				return err
			}
			w := cmd.OutOrStdout()
			if asJSON {
				out, err := json.MarshalIndent(items, "", "  ")
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(w, string(out))
				return nil
			}
			if len(items) == 0 {
				_, _ = fmt.Fprintln(w, "no backends registered")
				return nil
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			_, _ = fmt.Fprintln(tw, "NAME\tDOMAIN\tUPSTREAM\tREGISTERED")
			for _, it := range items {
				_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", it.Name, it.Domain, it.Upstream, it.RegisteredAt)
			}
			return tw.Flush()
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}
