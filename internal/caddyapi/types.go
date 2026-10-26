package caddyapi

// upstream is one reverse_proxy dial target.
type upstream struct {
	Dial string `json:"dial"`
}

// headerOps is the set of header operations applied to a request.
type headerOps struct {
	Set map[string][]string `json:"set"`
}

// headers wraps the request header operations of a handler.
type headers struct {
	Request headerOps `json:"request"`
}

// handler is Caddy's reverse_proxy handler.
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

// match is a host matcher for a route.
type match struct {
	Host []string `json:"host"`
}

// route is one Caddy route: an optional matcher and its handlers.
type route struct {
	// Match is omitted only for a catch-all route.
	Match []match `json:"match,omitempty"`
	// Handle holds handler or staticResponse values.
	Handle []any `json:"handle"`
}

// automaticHTTPS is a server's automatic HTTPS settings.
type automaticHTTPS struct {
	// DisableRedirects is a pointer so srv0 keeps emitting an explicit
	// false while lan-http omits it.
	DisableRedirects *bool `json:"disable_redirects,omitempty"`
	Disable          bool  `json:"disable,omitempty"`
}

// listenerWrapper is a Caddy listener wrapper such as proxy_protocol or tls.
type listenerWrapper struct {
	Wrapper        string `json:"wrapper"`
	FallbackPolicy string `json:"fallback_policy,omitempty"`
}

// sniMatch matches a TLS handshake by server name.
type sniMatch struct {
	SNI []string `json:"sni"`
}

// connectionPolicy is one TLS connection policy.
type connectionPolicy struct {
	Match sniMatch `json:"match"`
}

// server is one Caddy HTTP server.
type server struct {
	Listen                []string           `json:"listen"`
	ListenerWrappers      []listenerWrapper  `json:"listener_wrappers,omitempty"`
	Protocols             []string           `json:"protocols,omitempty"`
	Routes                []route            `json:"routes"`
	TLSConnectionPolicies []connectionPolicy `json:"tls_connection_policies,omitempty"`
	StrictSNIHost         bool               `json:"strict_sni_host,omitempty"`
	AutomaticHTTPS        automaticHTTPS     `json:"automatic_https"`
}

// httpApp is Caddy's http app: its named servers.
type httpApp struct {
	Servers map[string]server `json:"servers"`
}

// issuer is a certificate issuer module in an automation policy.
type issuer struct {
	Module string `json:"module"`
}

// policy is a TLS automation policy for the listed subjects.
type policy struct {
	Subjects []string `json:"subjects"`
	Issuers  []issuer `json:"issuers"`
}

// automation is the TLS automation settings.
type automation struct {
	Policies []policy `json:"policies"`
}

// pemPair is one certificate and key pair loaded as PEM.
type pemPair struct {
	Certificate string `json:"certificate"`
	Key         string `json:"key"`
}

// certificates holds the PEM pairs Caddy loads directly.
type certificates struct {
	LoadPEM []pemPair `json:"load_pem"`
}

// tlsApp is Caddy's tls app: automation and loaded certificates.
type tlsApp struct {
	Automation   automation    `json:"automation"`
	Certificates *certificates `json:"certificates,omitempty"`
}

// apps is the set of Caddy apps this config populates.
type apps struct {
	HTTP httpApp `json:"http"`
	TLS  tlsApp  `json:"tls"`
}

// adminConfig is Caddy's admin endpoint settings.
type adminConfig struct {
	Listen string `json:"listen"`
}

// config is the root of the Caddy admin-API JSON config.
type config struct {
	// Admin keeps the admin endpoint bound to 0.0.0.0:2019 across
	// /load swaps. Without it, Caddy reverts admin to its built-in
	// default (localhost:2019, container-loopback only) and the next
	// register/deregister can't reach the API from the host.
	Admin adminConfig `json:"admin"`
	Apps  apps        `json:"apps"`
}
