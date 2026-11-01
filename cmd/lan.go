package cmd

import (
	"bufio"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newLANCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "lan",
		Short: "Expose registered backends to the local network",
		Args:  noArgs,
		RunE:  showHelp,
	}
	c.AddCommand(newLANEnableCmd(), newLANDisableCmd())
	return c
}

func newLANEnableCmd() *cobra.Command {
	var ip, suffix string
	var yes bool
	c := &cobra.Command{
		Use:   "enable",
		Short: "Bind Caddy to a LAN address and serve LAN names",
		Long: `Bind a forwarder to this machine's LAN IP on ports 443 and 80 and serve
backends that have a LAN name. Without --ip the address is discovered; with
it the address is pinned.

--suffix is the DNS domain appended to a backend's name to form its LAN name:
with "--suffix mac.home.arpa", "register gamma --lan" serves
gamma.mac.home.arpa. Point a wildcard DNS A record for *.<suffix> at this
machine's LAN IP so every LAN name resolves. The suffix must be a lowercase
DNS name and cannot be "test" or end in ".test". Prefer home.arpa (reserved
for home networks), avoid .local (clashes with mDNS), and give each machine
on the network its own suffix. It is required the first time and is kept
afterwards; changing it does not rename existing LAN names.`,
		Example: `  caddy_shack lan enable --suffix mac.home.arpa
  caddy_shack lan enable --suffix mac.home.arpa --ip 192.168.1.20 --yes`,
		Args: cobra.NoArgs,
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			in := app.LANEnableInput{IP: ip, Suffix: suffix}
			if !yes {
				out := cmd.OutOrStdout()
				reader := bufio.NewReader(cmd.InOrStdin())
				in.Confirm = func(label string) bool {
					_, _ = fmt.Fprintf(out, "%s[y/N]: ", label)
					line, err := reader.ReadString('\n')
					if err != nil && line == "" {
						return false
					}
					ans := strings.ToLower(strings.TrimSpace(line))
					return ans == "y" || ans == "yes"
				}
			}
			res, err := a.LANEnable(cmd.Context(), in)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "LAN mode enabled on %s (%s), suffix %s\n", res.IP, res.Mode, res.Suffix)
			if res.Note != "" {
				_, _ = fmt.Fprintln(out, res.Note)
			}
			_, _ = fmt.Fprintf(out, "Point a DNS A record for *.%s (or each LAN name) at %s.\n", res.Suffix, res.IP)
			return nil
		}),
	}
	c.Flags().StringVar(&ip, "ip", "", "LAN IPv4 address to bind (default: discover)")
	c.Flags().StringVar(&suffix, "suffix", "", "Domain appended to backend names for LAN names, e.g. mac.home.arpa (required the first time)")
	c.Flags().BoolVar(&yes, "yes", false, "Do not ask for confirmation")
	return c
}

func newLANDisableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "disable",
		Short: "Stop LAN exposure (settings are kept)",
		Args:  cobra.NoArgs,
		RunE: withApp(func(cmd *cobra.Command, _ []string, a *app.App) error {
			if err := a.LANDisable(cmd.Context()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "LAN mode disabled")
			return nil
		}),
	}
}
