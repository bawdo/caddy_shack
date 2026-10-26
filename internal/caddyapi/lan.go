package caddyapi

import (
	"sort"
	"strconv"

	"github.com/bawdo/caddy_shack/internal/state"
)

// lanServers builds the srv-lan and lan-http servers plus the LAN TLS
// subjects the internal issuer must cover. hostRoutes is Build's route
// builder, so host-relay rewriting applies on the LAN too. covered holds the
// names served by loaded certificates, which are withheld from the subjects.
func lanServers(
	s *state.State,
	hostRoutes func([]string, map[string]state.Backend) []route,
	covered map[string]bool,
) (servers map[string]server, subjects []string) {
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
	lanSrv := server{
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
	lanHTTP := server{
		Listen:           []string{":" + strconv.Itoa(LANHTTPListenPort)},
		ListenerWrappers: []listenerWrapper{proxyProtocol},
		Protocols:        []string{"h1"},
		Routes:           httpRoutes,
		AutomaticHTTPS:   automaticHTTPS{Disable: true},
	}
	return map[string]server{"srv-lan": lanSrv, "lan-http": lanHTTP}, subjects
}
