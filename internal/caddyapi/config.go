package caddyapi

import (
	"encoding/json"
	"net"
	"sort"
	"strconv"
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

// Build renders the full Caddy admin-API JSON config for the given State.
// Routes are sorted by domain for deterministic output (important for
// golden-file tests and predictable Caddy /load behaviour).
func Build(s *state.State) []byte {
	return BuildWith(s, Options{})
}

// BuildWith is Build with Options applied. BuildWith(s, Options{}) is
// byte-for-byte Build(s).
func BuildWith(s *state.State, opts Options) []byte {
	certs := opts.Certs
	type upstream struct {
		Dial string `json:"dial"`
	}
	type headerOps struct {
		Set map[string][]string `json:"set"`
	}
	type headers struct {
		Request headerOps `json:"request"`
	}
	type handler struct {
		Handler   string     `json:"handler"`
		Upstreams []upstream `json:"upstreams"`
		// Headers is only emitted for relayed host-mode upstreams.
		Headers *headers `json:"headers,omitempty"`
	}
	// staticResponse is Caddy's static_response handler. Abort closes the
	// connection without writing a response.
	type staticResponse struct {
		Handler    string              `json:"handler"`
		StatusCode int                 `json:"status_code,omitempty"`
		Headers    map[string][]string `json:"headers,omitempty"`
		Abort      bool                `json:"abort,omitempty"`
	}
	type match struct {
		Host []string `json:"host"`
	}
	type route struct {
		// Match is omitted only for a catch-all route.
		Match []match `json:"match,omitempty"`
		// Handle holds handler or staticResponse values.
		Handle []any `json:"handle"`
	}
	type automaticHTTPS struct {
		// DisableRedirects is a pointer so srv0 keeps emitting an explicit
		// false while lan-http omits it.
		DisableRedirects *bool `json:"disable_redirects,omitempty"`
		Disable          bool  `json:"disable,omitempty"`
	}
	type listenerWrapper struct {
		Wrapper        string `json:"wrapper"`
		FallbackPolicy string `json:"fallback_policy,omitempty"`
	}
	type sniMatch struct {
		SNI []string `json:"sni"`
	}
	type connectionPolicy struct {
		Match sniMatch `json:"match"`
	}
	type server struct {
		Listen                []string           `json:"listen"`
		ListenerWrappers      []listenerWrapper  `json:"listener_wrappers,omitempty"`
		Protocols             []string           `json:"protocols,omitempty"`
		Routes                []route            `json:"routes"`
		TLSConnectionPolicies []connectionPolicy `json:"tls_connection_policies,omitempty"`
		StrictSNIHost         bool               `json:"strict_sni_host,omitempty"`
		AutomaticHTTPS        automaticHTTPS     `json:"automatic_https"`
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
		lanNames := make([]string, 0, len(s.Backends))
		byLANName := make(map[string]state.Backend, len(s.Backends))
		for _, b := range s.Backends {
			if b.LANDomain == "" {
				continue
			}
			if _, dup := byLANName[b.LANDomain]; !dup {
				lanNames = append(lanNames, b.LANDomain)
			}
			byLANName[b.LANDomain] = b
		}
		sort.Strings(lanNames)

		// lanHosts is every LAN name and its wildcard. It drives the SNI
		// policy, the lan-http redirect and the TLS subjects.
		lanHosts := make([]string, 0, len(lanNames)*2)
		for _, n := range lanNames {
			lanHosts = append(lanHosts, n, "*."+n)
		}
		for _, h := range lanHosts {
			if !isSubjectCovered(h, covered) {
				subjects = append(subjects, h)
			}
		}

		// An empty sni list matches every handshake in Caddy, so with no
		// LAN names the policy matches a name that can never be sent.
		sni := lanHosts
		if len(sni) == 0 {
			sni = []string{"lan.invalid"}
		}

		// Isolation from .test names on srv-lan has three layers: the SNI
		// policy (no catch-all, so other handshakes fail), strict_sni_host
		// (no LAN SNI with a .test Host header) and a terminal abort route.
		// The PROXY header precedes the TLS handshake, so proxy_protocol
		// must wrap before tls.
		//
		// Protocols is set only on the LAN servers: the forwarder carries
		// TCP alone, so they must not advertise HTTP/3 on a UDP port nobody
		// publishes. srv0 omits it and keeps Caddy's defaults.
		proxyProtocol := listenerWrapper{Wrapper: "proxy_protocol", FallbackPolicy: "require"}
		lanRoutes := append(hostRoutes(lanNames, byLANName), route{
			Handle: []any{staticResponse{Handler: "static_response", Abort: true}},
		})
		servers["srv-lan"] = server{
			Listen:                []string{":" + strconv.Itoa(LANHTTPSListenPort)},
			ListenerWrappers:      []listenerWrapper{proxyProtocol, {Wrapper: "tls"}},
			Protocols:             []string{"h1", "h2"},
			Routes:                lanRoutes,
			TLSConnectionPolicies: []connectionPolicy{{Match: sniMatch{SNI: sni}}},
			StrictSNIHost:         true,
			AutomaticHTTPS:        automaticHTTPS{DisableRedirects: boolPtr(true)},
		}

		// lan-http redirects LAN names to HTTPS and 404s everything else.
		// Automatic HTTPS is off, or Caddy would enable TLS on this
		// host-matched server because it is not on the HTTP port.
		httpRoutes := make([]route, 0, 2)
		if len(lanHosts) > 0 {
			httpRoutes = append(httpRoutes, route{
				Match: []match{{Host: lanHosts}},
				Handle: []any{staticResponse{
					Handler:    "static_response",
					StatusCode: 308,
					Headers:    map[string][]string{"Location": {"https://{http.request.host}{http.request.uri}"}},
				}},
			})
		}
		httpRoutes = append(httpRoutes, route{
			Handle: []any{staticResponse{Handler: "static_response", StatusCode: 404}},
		})
		servers["lan-http"] = server{
			Listen:           []string{":" + strconv.Itoa(LANHTTPListenPort)},
			ListenerWrappers: []listenerWrapper{proxyProtocol},
			Protocols:        []string{"h1"},
			Routes:           httpRoutes,
			AutomaticHTTPS:   automaticHTTPS{Disable: true},
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
				Servers: servers,
			},
			TLS: tls,
		},
	}

	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}

func boolPtr(b bool) *bool { return &b }
