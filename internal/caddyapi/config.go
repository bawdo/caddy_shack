package caddyapi

import (
	"encoding/json"
	"sort"

	"github.com/bawdo/caddy_shack/internal/state"
)

// Build renders the full Caddy admin-API JSON config for the given State.
// Routes are sorted by domain for deterministic output (important for
// golden-file tests and predictable Caddy /load behaviour).
func Build(s *state.State) []byte {
	type upstream struct {
		Dial string `json:"dial"`
	}
	type handler struct {
		Handler   string     `json:"handler"`
		Upstreams []upstream `json:"upstreams"`
	}
	type match struct {
		Host []string `json:"host"`
	}
	type route struct {
		Match  []match   `json:"match"`
		Handle []handler `json:"handle"`
	}
	type automaticHTTPS struct {
		DisableRedirects bool `json:"disable_redirects"`
	}
	type server struct {
		Listen         []string       `json:"listen"`
		Routes         []route        `json:"routes"`
		AutomaticHTTPS automaticHTTPS `json:"automatic_https"`
	}
	type httpApp struct {
		Servers map[string]server `json:"servers"`
	}
	type issuer struct {
		Module string `json:"module"`
	}
	type policy struct {
		Subjects []string `json:"subjects"`
		Issuers  []issuer `json:"issuers"`
	}
	type automation struct {
		Policies []policy `json:"policies"`
	}
	type tlsApp struct {
		Automation automation `json:"automation"`
	}
	type apps struct {
		HTTP httpApp `json:"http"`
		TLS  tlsApp  `json:"tls"`
	}
	type adminConfig struct {
		Listen string `json:"listen"`
	}
	type config struct {
		// Admin keeps the admin endpoint bound to 0.0.0.0:2019 across
		// /load swaps. Without it, Caddy reverts admin to its built-in
		// default (localhost:2019, container-loopback only) and the next
		// register/deregister can't reach the API from the host.
		Admin adminConfig `json:"admin"`
		Apps  apps        `json:"apps"`
	}

	domains := make([]string, 0, len(s.Backends))
	byDomain := make(map[string]state.Backend, len(s.Backends))
	for _, b := range s.Backends {
		domains = append(domains, b.Domain)
		byDomain[b.Domain] = b
	}
	sort.Strings(domains)

	routes := make([]route, 0, len(domains))
	for _, d := range domains {
		b := byDomain[d]
		routes = append(routes, route{
			Match: []match{{Host: []string{d}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
	}

	cfg := config{
		Admin: adminConfig{Listen: "0.0.0.0:2019"},
		Apps: apps{
			HTTP: httpApp{
				Servers: map[string]server{
					"srv0": {
						Listen:         []string{":443"},
						Routes:         routes,
						AutomaticHTTPS: automaticHTTPS{DisableRedirects: false},
					},
				},
			},
			TLS: tlsApp{
				Automation: automation{
					Policies: []policy{{
						Subjects: []string{"*.test"},
						Issuers:  []issuer{{Module: "internal"}},
					}},
				},
			},
		},
	}

	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}
