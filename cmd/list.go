package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List registered backends and whether Caddy is serving them",
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			items, err := a.List(cmd.Context())
			if err != nil {
				return err
			}
			return renderList(cmd.OutOrStdout(), items, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Output as JSON")
	return c
}

// renderList writes the human or JSON form of the listing to w. Extracted
// from RunE so the formatting can be tested without spinning up a real App.
func renderList(w io.Writer, items []app.Listing, asJSON bool) error {
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
	_, _ = fmt.Fprintln(tw, "NAME\tDOMAIN\tLAN\tUPSTREAM\tREGISTERED\tLIVE")
	notLive := 0
	for _, it := range items {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", it.Name, it.Domain, it.LANDomain, it.Upstream, it.RegisteredAt, it.Live)
		if it.Live == app.LiveNo || it.Live == app.LiveStale {
			notLive++
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "Each backend also serves *.<domain> unless a more specific override exists.")
	if notLive > 0 {
		_, _ = fmt.Fprintf(w, "%d backend(s) registered but not live in Caddy; run 'caddy_shack sync'.\n", notLive)
	}
	return nil
}
