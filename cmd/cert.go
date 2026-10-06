package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
)

// cliPrompter shows the DNS records on the command's own streams and waits
// for the operator. It takes its reader and writer as fields rather than
// reaching for os.Stdin so the prompt can be driven in tests through
// cmd.SetIn.
type cliPrompter struct {
	in  io.Reader
	out io.Writer
}

// Confirm prints every record in one block and blocks until the operator
// presses Enter. Every record is shown at once because a wildcard order puts
// two challenges on the same name, and both must exist together.
func (p *cliPrompter) Confirm(records []acme.Record) error {
	_, _ = fmt.Fprintln(p.out, "\nAdd the following DNS records at your provider:")
	_, _ = fmt.Fprintln(p.out)
	for _, r := range records {
		_, _ = fmt.Fprintf(p.out, "  %s  60  IN  TXT  %q\n", strings.TrimSuffix(r.FQDN, "."), r.Value)
	}
	_, _ = fmt.Fprintln(p.out)
	if len(records) > 1 {
		_, _ = fmt.Fprintln(p.out, "Both records share one name and must exist at the same time.")
		_, _ = fmt.Fprintln(p.out, "Do not replace the first when you add the second.")
		_, _ = fmt.Fprintln(p.out)
	}
	_, _ = fmt.Fprint(p.out, "Press Enter once the records are saved: ")

	reader := bufio.NewReader(p.in)
	if _, err := reader.ReadString('\n'); err != nil {
		return fmt.Errorf("no confirmation received: %w", err)
	}
	_, _ = fmt.Fprintln(p.out, "Checking the authoritative nameservers, this can take a minute.")
	return nil
}

// newCertCmd builds the cert command group, which requests, lists, renews
// and removes Let's Encrypt certificates for domains this machine controls.
func newCertCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "cert",
		Short: "Manage Let's Encrypt certificates for public domains",
		Long: `Obtain and manage publicly trusted Let's Encrypt certificates for domains
you control, alongside the internal CA that serves .test.

Certificates are requested with a manual DNS-01 challenge: caddy_shack shows
you the TXT records to add at your DNS provider and waits while you add them.
That is the only challenge type available when a domain's public A record
points at 127.0.0.1, because Let's Encrypt validates from its own servers and
cannot reach a loopback address.

Each request covers the domain and its single-label subdomains, so a
certificate for alpha.arrix.dev also serves vite.alpha.arrix.dev.`,
	}
	c.AddCommand(newCertRequestCmd())
	c.AddCommand(newCertListCmd())
	c.AddCommand(newCertRenewCmd())
	c.AddCommand(newCertRemoveCmd())
	return c
}

// newCertRequestCmd builds the "cert request" subcommand.
func newCertRequestCmd() *cobra.Command {
	var (
		email      string
		production bool
	)
	c := &cobra.Command{
		Use:   "request <domain>",
		Short: "Obtain a certificate for a domain and its subdomains",
		Long: `Obtain a Let's Encrypt certificate covering <domain> and *.<domain>, then
load it into the running Caddy.

Requests go to the Let's Encrypt staging environment by default. Staging
certificates are not trusted by browsers, so use them to prove the DNS flow
works, then re-run with --production. Production allows only five duplicate
certificates per week for the same set of names, so a failed attempt against
it is expensive.

The certificate and key are stored on this machine and re-pushed after an
upgrade or a container recreate, so they survive both.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			if !production {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(),
					"Using the Let's Encrypt staging environment. The certificate will not be\n"+
						"trusted by browsers. Re-run with --production for a trusted certificate.")
			}
			prompt := &cliPrompter{in: cmd.InOrStdin(), out: cmd.ErrOrStderr()}
			if err := a.CertRequest(cmd.Context(), app.CertRequestInput{
				Domain:     args[0],
				Email:      email,
				Production: production,
			}, prompt); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
				"\nCertificate installed for %s and *.%s.\nYou can now remove the _acme-challenge TXT records.\n",
				args[0], args[0])
			return nil
		},
	}
	c.Flags().StringVar(&email, "email", "", "ACME account contact address (required)")
	c.Flags().BoolVar(&production, "production", false,
		"Use the real Let's Encrypt directory instead of staging")
	_ = c.MarkFlagRequired("email")
	return c
}

// newCertListCmd builds the "cert list" subcommand.
func newCertListCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the stored certificates and when they expire",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			rows, err := a.CertList(cmd.Context())
			if err != nil {
				return err
			}
			return renderCertList(cmd.OutOrStdout(), rows, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "Emit JSON instead of a table")
	return c
}

// newCertRenewCmd builds the "cert renew" subcommand.
func newCertRenewCmd() *cobra.Command {
	var force bool
	c := &cobra.Command{
		Use:   "renew <domain>",
		Short: "Renew a certificate that is within 30 days of expiry",
		Long: `Renew a stored certificate. A no-op unless the certificate has fewer than
30 days left, so it is safe to run repeatedly. Pass --force to renew anyway.

Renewal uses the same DNS-01 prompt as request, and renews into whichever
environment the certificate came from.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			prompt := &cliPrompter{in: cmd.InOrStdin(), out: cmd.ErrOrStderr()}
			renewed, err := a.CertRenew(cmd.Context(), args[0], force, prompt)
			if err != nil {
				return err
			}
			if !renewed {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
					"%s is not due for renewal. Use --force to renew anyway.\n", args[0])
				return nil
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Renewed %s.\n", args[0])
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "Renew even when the certificate is not due")
	return c
}

// newCertRemoveCmd builds the "cert remove" subcommand.
func newCertRemoveCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "remove <domain>",
		Short: "Delete a stored certificate and stop serving it",
		Long: `Delete a stored certificate and push a config without it.

Idempotent: removing a domain with no certificate exits 0 with a note on
stderr. The domain's routes are untouched, use deregister for those.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			removed, err := a.CertRemove(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if !removed {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "no certificate for %s\n", args[0])
				return nil
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Removed the certificate for %s.\n", args[0])
			return nil
		},
	}
	return c
}

// renderCertList writes the listing as a table, or as JSON when asJSON is
// true. Empty JSON output is an array rather than null so scripts can
// iterate the result without a nil check.
func renderCertList(w io.Writer, rows []app.CertListing, asJSON bool) error {
	if asJSON {
		if rows == nil {
			rows = []app.CertListing{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			return fmt.Errorf("encode json: %w", err)
		}
		return nil
	}
	if len(rows) == 0 {
		_, _ = fmt.Fprintln(w, "no certificates")
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "DOMAIN\tENV\tEXPIRES\tDAYS LEFT\tSUBJECTS")
	for _, r := range rows {
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			r.Domain, r.Env, r.NotAfter, r.DaysRemaining, strings.Join(r.Subjects, ", "))
	}
	return tw.Flush()
}
