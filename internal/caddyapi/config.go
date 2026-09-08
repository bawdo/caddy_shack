package caddyapi

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/bawdo/caddy_shack/internal/state"
)

// LoadedCert is one externally-issued certificate to hand Caddy directly.
// Subjects are the DNS names it covers; they are withheld from the internal
// issuer's automation policy so Caddy serves this certificate rather than
// minting its own over the top.
type LoadedCert struct {
	Subjects []string
	CertPEM  string
	KeyPEM   string
}

// isSubjectCovered reports whether a plain (non-wildcard) subject is served
// by a loaded certificate: either the subject itself is a literal covered
// name, or its immediate parent wildcard (e.g. *.alpha.arrix.dev for
// vite.alpha.arrix.dev) is. A Caddy `*` matches exactly one label, so the
// parent check must not recurse: a.b.alpha.arrix.dev is not covered by
// *.alpha.arrix.dev. This subsumption only applies to plain subjects; a
// wildcard subject is covered only by an exact match, never by its parent.
func isSubjectCovered(subject string, covered map[string]bool) bool {
	if covered[subject] {
		return true
	}
	if strings.HasPrefix(subject, "*.") {
		return false
	}
	if i := strings.Index(subject, "."); i >= 0 {
		if covered["*."+subject[i+1:]] {
			return true
		}
	}
	return false
}

// Build renders the full Caddy admin-API JSON config for the given State and
// the certificates to load directly. Routes are sorted by domain for
// deterministic output (important for golden-file tests and predictable Caddy
// /load behaviour). Passing a nil certs slice reproduces the internal-issuer
// only config.
func Build(s *state.State, certs []LoadedCert) []byte {
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
	type pemPair struct {
		Certificate string `json:"certificate"`
		Key         string `json:"key"`
	}
	type certificates struct {
		LoadPEM []pemPair `json:"load_pem"`
	}
	type tlsApp struct {
		Automation   automation    `json:"automation"`
		Certificates *certificates `json:"certificates,omitempty"`
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

	// Each backend yields an exact route (D) and a wildcard route (*.D) to
	// the same upstream. All exact routes are emitted before all wildcard
	// routes: a Caddy `*` matches exactly one label, so a request like
	// bar.foo.test matches both the exact bar.foo.test route and the
	// *.foo.test wildcard. reverse_proxy is terminal, so ordering every
	// exact ahead of every wildcard guarantees an explicit override wins.
	exactRoutes := make([]route, 0, len(domains))
	wildcardRoutes := make([]route, 0, len(domains))
	for _, d := range domains {
		b := byDomain[d]
		wildcard := "*." + d
		exactRoutes = append(exactRoutes, route{
			Match: []match{{Host: []string{d}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
		wildcardRoutes = append(wildcardRoutes, route{
			Match: []match{{Host: []string{wildcard}}},
			Handle: []handler{{
				Handler:   "reverse_proxy",
				Upstreams: []upstream{{Dial: b.Upstream}},
			}},
		})
	}
	routes := append(exactRoutes[:len(exactRoutes):len(exactRoutes)], wildcardRoutes...)

	// Subjects covered by a loaded certificate are withheld from the
	// internal issuer. Caddy's automatic HTTPS skips a hostname that already
	// has a manually loaded certificate, but leaving the name in an explicit
	// internal policy would ask Caddy to manage it regardless.
	covered := make(map[string]bool)
	for _, c := range certs {
		for _, sub := range c.Subjects {
			covered[sub] = true
		}
	}

	// TLS subjects are derived from the registered names, not a static
	// *.test: a single-label wildcard can't cover bar.foo.test or a
	// *.foo.test wildcard cert. We mint certs only for names actually in use.
	subjects := make([]string, 0, len(domains)*2)
	for _, d := range domains {
		if !isSubjectCovered(d, covered) {
			subjects = append(subjects, d)
		}
		if w := "*." + d; !covered[w] {
			subjects = append(subjects, w)
		}
	}
	sort.Strings(subjects)

	// An automation policy with an empty subjects list means "every
	// hostname", which would hand every name to the internal issuer. Emit no
	// policy in that case, unless there were no domains to begin with: that
	// matches the config Build has always produced for an empty state, where
	// the empty policy is harmless because there are no routes to protect.
	policies := []policy{}
	if len(subjects) > 0 || len(domains) == 0 {
		policies = append(policies, policy{
			Subjects: subjects,
			Issuers:  []issuer{{Module: "internal"}},
		})
	}

	loadPEM := make([]pemPair, 0, len(certs))
	for _, c := range certs {
		loadPEM = append(loadPEM, pemPair{Certificate: c.CertPEM, Key: c.KeyPEM})
	}

	tls := tlsApp{Automation: automation{Policies: policies}}
	if len(loadPEM) > 0 {
		tls.Certificates = &certificates{LoadPEM: loadPEM}
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
			TLS: tls,
		},
	}

	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}
