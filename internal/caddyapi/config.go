package caddyapi

import (
	"encoding/json"
	"net"
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

// LAN listener ports inside the main container. The forwarder container
// relays the LAN's 443 and 80 to these, so neither is published directly.
const (
	LANHTTPSListenPort = 8443
	LANHTTPListenPort  = 8080
)

// Options adjusts how Build renders upstreams. The zero value renders every
// upstream exactly as stored in state (the Docker Desktop behaviour).
type Options struct {
	// HostRelay, when non-nil, reroutes host-mode backends (upstreams whose
	// host is HostRelay.Host) through a relay instead of dialling them
	// directly. Container-mode upstreams are never touched.
	HostRelay *HostRelay
	// Certs are externally-issued certificates to load directly. Their
	// subjects are withheld from the internal issuer. Nil reproduces the
	// internal-issuer only config.
	Certs []LoadedCert
}

// HostRelay describes the native-Linux relay: a Caddy in the host's network
// namespace listening on a unix socket shared with the main container, which
// forwards each request to 127.0.0.1:<value of PortHeader>.
type HostRelay struct {
	// Host is the upstream host that marks a backend as host-mode
	// (host.docker.internal).
	Host string
	// Dial is the relay's address, e.g. "unix//run/caddy_shack/relay.sock".
	Dial string
	// PortHeader carries the backend's host port to the relay. It is set
	// (not appended), so a client-supplied value can never choose the port.
	PortHeader string
}

// Build renders the full Caddy admin-API JSON config for the given State,
// with opts applied. Routes are sorted by domain for deterministic output
// (important for golden-file tests and predictable Caddy /load behaviour).
func Build(s *state.State, opts Options) []byte {
	certs := opts.Certs
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
	proxy := func(b state.Backend) handler {
		h := handler{
			Handler:   "reverse_proxy",
			Upstreams: []upstream{{Dial: b.Upstream}},
		}
		if r := opts.HostRelay; r != nil {
			if host, port, err := net.SplitHostPort(b.Upstream); err == nil && host == r.Host {
				h.Upstreams = []upstream{{Dial: r.Dial}}
				h.Headers = &headers{Request: headerOps{Set: map[string][]string{r.PortHeader: {port}}}}
			}
		}
		return h
	}
	// hostRoutes builds the exact-then-wildcard routes for the given sorted
	// names. srv0 feeds it .test domains, srv-lan feeds it LAN names; both
	// share proxy, so host-relay rewriting applies on the LAN too.
	hostRoutes := func(names []string, byName map[string]state.Backend) []route {
		exactRoutes := make([]route, 0, len(names))
		wildcardRoutes := make([]route, 0, len(names))
		for _, d := range names {
			b := byName[d]
			wildcard := "*." + d
			exactRoutes = append(exactRoutes, route{
				Match:  []match{{Host: []string{d}}},
				Handle: []any{proxy(b)},
			})
			wildcardRoutes = append(wildcardRoutes, route{
				Match:  []match{{Host: []string{wildcard}}},
				Handle: []any{proxy(b)},
			})
		}
		return append(exactRoutes[:len(exactRoutes):len(exactRoutes)], wildcardRoutes...)
	}
	routes := hostRoutes(domains, byDomain)

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

	servers := map[string]server{
		"srv0": {
			Listen:         []string{":443"},
			Routes:         routes,
			AutomaticHTTPS: automaticHTTPS{DisableRedirects: boolPtr(false)},
		},
	}

	if s.LANEnabled() {
		lanSrvs, lanSubjects := lanServers(s, hostRoutes, covered)
		for name, srv := range lanSrvs {
			servers[name] = srv
		}
		subjects = append(subjects, lanSubjects...)
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
		Admin: adminConfig{
			Listen:        "0.0.0.0:2019",
			EnforceOrigin: true,
			Origins:       AdminOrigins,
		},
		Apps: apps{
			HTTP: httpApp{
				Servers: servers,
			},
			TLS: tls,
		},
	}

	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}

func boolPtr(b bool) *bool { return &b }
