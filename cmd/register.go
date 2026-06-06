package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRegisterCmd() *cobra.Command {
	var (
		port   int
		domain string
	)
	c := &cobra.Command{
		Use:   "register <name>",
		Short: "Register a backend so Caddy serves it over HTTPS at <name>.test",
		Long: `Register a backend so Caddy reverse-proxies https://<name>.test to a
service listening on the given host port.

Caddy itself runs in a container; this command always routes through
host.docker.internal, so all you supply is the port your service listens on
locally. Whether your service binds 127.0.0.1, 0.0.0.0, or localhost on the
host doesn't matter - the port is what we need.

Subdomains: registering a backend automatically serves every subdomain of
its domain from the same upstream, so "register foo --port 2000" makes
foo.test and *.foo.test (bar.foo.test, etc.) all reach port 2000. To send one
subdomain elsewhere, register it explicitly with a dotted name:
"register bar.foo --port 3000".`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Register(cmd.Context(), app.RegisterInput{
				Name:   args[0],
				Domain: domain,
				Port:   port,
			})
		},
	}
	c.Flags().IntVar(&port, "port", 0, "Host port the upstream service listens on (required)")
	c.Flags().StringVar(&domain, "domain", "", "Optional domain (defaults to <name>.test)")
	_ = c.MarkFlagRequired("port")
	return c
}
