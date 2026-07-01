package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newCACmd() *cobra.Command {
	var intermediate bool
	c := &cobra.Command{
		Use:     "ca",
		Aliases: []string{"export"},
		Short:   "Print the local root CA certificate (PEM) to stdout",
		Long: `Print caddy_shack's local root CA certificate, PEM-encoded, to stdout so
other tools and containers can trust https://*.test without reaching into
Caddy's internal PKI paths. Only the PEM is written to stdout, so it is safe
to redirect or pipe:

  caddy_shack ca > root.crt
  caddy_shack ca | docker exec -i somecontainer tee /usr/local/share/ca-certificates/caddy_shack.crt

Pass --intermediate to append the intermediate certificate after the root.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			pem, err := a.ExportCA(cmd.Context(), intermediate)
			if err != nil {
				return err
			}
			writeCA(cmd.OutOrStdout(), pem)
			return nil
		},
	}
	c.Flags().BoolVar(&intermediate, "intermediate", false, "Also emit the intermediate certificate (root first)")
	return c
}

// writeCA writes the PEM verbatim to w. Kept separate from RunE so the
// "stdout is PEM only" contract can be tested without a real App. The PEM
// already carries its trailing newline, so nothing is added.
func writeCA(w io.Writer, pem string) {
	_, _ = fmt.Fprint(w, pem)
}
