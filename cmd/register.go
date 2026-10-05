package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRegisterCmd() *cobra.Command {
	var (
		port      int
		domain    string
		container string
	)
	c := &cobra.Command{
		Use:   "register <name>",
		Short: "Register a backend so Caddy serves it over HTTPS at <name>.test",
		Long: `Register a backend so Caddy reverse-proxies https://<name>.test to a
service listening on the given host port.

Caddy itself runs in a container, so all you supply is the port your
service listens on locally. How Caddy reaches that port depends on the
Docker engine:

  - Docker Desktop, OrbStack, Colima, Lima, Rancher Desktop: via
    host.docker.internal, which the engine forwards to the host's loopback.
  - Native Docker Engine on Linux: via the caddy_shack_relay container,
    which runs on the host network and dials localhost:<port> over a
    private unix socket (no extra TCP listener, no firewall rule needed).

Either way, binding 127.0.0.1, 0.0.0.0, or localhost on the host all work.
On Linux, if 'caddy_shack doctor' reports container-config failing, run
'caddy_shack install' once to recreate the container with the relay.

Subdomains: registering a backend automatically serves every subdomain of
its domain from the same upstream, so "register foo --port 2000" makes
foo.test and *.foo.test (bar.foo.test, etc.) all reach port 2000. To send one
subdomain elsewhere, register it explicitly with a dotted name:
"register bar.foo --port 3000".

Container mode: pass --container <alias> to route to a container reachable on
the shared "caddy_shack" Docker network instead of the host (same on every
engine). The app's compose must attach the service to that external network
with the given alias. --port is still required (the container's listening
port).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Register(cmd.Context(), app.RegisterInput{
				Name:      args[0],
				Domain:    domain,
				Port:      port,
				Container: container,
			})
		},
	}
	c.Flags().IntVar(&port, "port", 0, "Host port the upstream service listens on (required)")
	c.Flags().StringVar(&domain, "domain", "", "Optional domain (defaults to <name>.test)")
	c.Flags().StringVar(&container, "container", "",
		"Container network alias to route to over the shared caddy_shack network "+
			"(instead of the host). Requires --port.")
	_ = c.MarkFlagRequired("port")
	return c
}
