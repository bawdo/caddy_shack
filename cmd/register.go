package cmd

import (
	"github.com/spf13/cobra"

	"github.com/bawdo/caddy_shack/internal/app"
)

func newRegisterCmd() *cobra.Command {
	var upstream, domain string
	c := &cobra.Command{
		Use:   "register <name>",
		Short: "Register a backend so Caddy serves it over HTTPS at <name>.test",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			return a.Register(cmd.Context(), app.RegisterInput{
				Name:     args[0],
				Domain:   domain,
				Upstream: upstream,
			})
		},
	}
	c.Flags().StringVar(&upstream, "upstream", "", "Upstream host:port (required)")
	c.Flags().StringVar(&domain, "domain", "", "Optional domain (defaults to <name>.test)")
	_ = c.MarkFlagRequired("upstream")
	return c
}
