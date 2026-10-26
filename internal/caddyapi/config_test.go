package caddyapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigEmptyState(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}
	got := Build(s, Options{})
	assertJSONMatchesGolden(t, got, "empty.json")
}

func TestBuildConfigOneBackend(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s, Options{})
	assertJSONMatchesGolden(t, got, "one_backend.json")
}

// TestBuildPreservesAdminBinding pins down that every rendered config keeps
// admin bound to 0.0.0.0:2019. Without this, POST /load reverts admin to
// caddy's default localhost:2019 and the next register/deregister can't
// reach the container's admin API from the host. Discovered by smoke test.
func TestBuildPreservesAdminBinding(t *testing.T) {
	got := Build(&state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}, Options{})
	var doc struct {
		Admin struct {
			Listen string `json:"listen"`
		} `json:"admin"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if doc.Admin.Listen != "0.0.0.0:2019" {
		t.Errorf("admin.listen: got %q want 0.0.0.0:2019", doc.Admin.Listen)
	}
}

func TestBuildConfigTwoBackendsSortedByDomain(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web": {Domain: "web.test", Upstream: "127.0.0.1:3000"},
			"api": {Domain: "api.test", Upstream: "127.0.0.1:4000"},
		},
	}
	got := Build(s, Options{})
	assertJSONMatchesGolden(t, got, "two_backends.json")
}

func TestBuildConfigOverrideExactBeatsWildcard(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo":     {Domain: "foo.test", Upstream: "127.0.0.1:2000"},
			"bar.foo": {Domain: "bar.foo.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s, Options{})
	assertJSONMatchesGolden(t, got, "override.json")
}

// TestBuildOrdersExactRoutesBeforeWildcards pins the invariant that an exact
// override route appears before the parent's wildcard route. Caddy evaluates
// routes in order and reverse_proxy is terminal, so a request to
// bar.foo.test must hit the exact route (port 3000) before *.foo.test.
func TestBuildOrdersExactRoutesBeforeWildcards(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"foo":     {Domain: "foo.test", Upstream: "127.0.0.1:2000"},
			"bar.foo": {Domain: "bar.foo.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s, Options{})

	var doc struct {
		Apps struct {
			HTTP struct {
				Servers struct {
					Srv0 struct {
						Routes []struct {
							Match []struct {
								Host []string `json:"host"`
							} `json:"match"`
						} `json:"routes"`
					} `json:"srv0"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	indexOf := func(host string) int {
		for i, r := range doc.Apps.HTTP.Servers.Srv0.Routes {
			if len(r.Match) == 1 && len(r.Match[0].Host) == 1 && r.Match[0].Host[0] == host {
				return i
			}
		}
		return -1
	}
	exact := indexOf("bar.foo.test")
	wildcard := indexOf("*.foo.test")
	if exact == -1 || wildcard == -1 {
		t.Fatalf("missing routes: exact=%d wildcard=%d", exact, wildcard)
	}
	if exact > wildcard {
		t.Errorf("exact bar.foo.test (idx %d) must precede *.foo.test (idx %d)", exact, wildcard)
	}
}

func TestBuildConfigContainerUpstream(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"app": {Domain: "app.test", Upstream: "app:3000"},
		},
	}
	got := Build(s, Options{})
	assertJSONMatchesGolden(t, got, "one_container_backend.json")
}

func assertJSONMatchesGolden(t *testing.T, got []byte, name string) {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var gotAny, wantAny any
	if err := json.Unmarshal(got, &gotAny); err != nil {
		t.Fatalf("unmarshal got: %v", err)
	}
	if err := json.Unmarshal(want, &wantAny); err != nil {
		t.Fatalf("unmarshal want: %v", err)
	}
	gotNorm, _ := json.MarshalIndent(gotAny, "", "  ")
	wantNorm, _ := json.MarshalIndent(wantAny, "", "  ")
	if string(gotNorm) != string(wantNorm) {
		t.Errorf("golden mismatch for %s\nwant:\n%s\n\ngot:\n%s", name, wantNorm, gotNorm)
	}
}

func TestBuildWithNoCertsIsUnchanged(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
		},
		Certs: map[string]state.Cert{},
	}
	got := Build(s, Options{})

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	if _, present := tlsApp["certificates"]; present {
		t.Error("with no certs, the config must not carry a certificates block")
	}
}

func TestBuildLoadsCertPEMAndExcludesItsSubjects(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
			"web":   {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n",
		KeyPEM:   "-----BEGIN EC PRIVATE KEY-----\nKEY\n-----END EC PRIVATE KEY-----\n",
	}}
	got := Build(s, Options{Certs: certs})

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)

	loadPEM, ok := tlsApp["certificates"].(map[string]any)["load_pem"].([]any)
	if !ok || len(loadPEM) != 1 {
		t.Fatalf("expected exactly one load_pem entry, got %v", tlsApp["certificates"])
	}
	entry := loadPEM[0].(map[string]any)
	if entry["certificate"] != certs[0].CertPEM {
		t.Errorf("certificate = %q, want the chain PEM verbatim", entry["certificate"])
	}
	if entry["key"] != certs[0].KeyPEM {
		t.Errorf("key = %q, want the key PEM verbatim", entry["key"])
	}

	// The internal issuer must not claim the LE subjects, or Caddy would
	// mint an internal certificate that shadows the loaded one.
	policies := tlsApp["automation"].(map[string]any)["policies"].([]any)
	subjects := policies[0].(map[string]any)["subjects"].([]any)
	for _, sub := range subjects {
		if sub == "alpha.arrix.dev" || sub == "*.alpha.arrix.dev" {
			t.Errorf("internal policy must not claim %q, it has a loaded certificate", sub)
		}
	}
	// The .test subjects must still be there.
	var sawTest bool
	for _, sub := range subjects {
		if sub == "hello.test" {
			sawTest = true
		}
	}
	if !sawTest {
		t.Error("internal policy lost hello.test")
	}
}

func TestBuildKeepsRoutesForCertCoveredDomains(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"web": {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, Options{Certs: certs})

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	routes := cfg["apps"].(map[string]any)["http"].(map[string]any)["servers"].(map[string]any)["srv0"].(map[string]any)["routes"].([]any)
	if len(routes) != 2 {
		t.Fatalf("expected exact + wildcard routes, got %d", len(routes))
	}
}

func TestBuildOmitsAutomationPolicyWhenEveryDomainHasACert(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"web": {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, Options{Certs: certs})

	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	automation, present := tlsApp["automation"]
	if present {
		policies := automation.(map[string]any)["policies"]
		if policies != nil && len(policies.([]any)) != 0 {
			t.Errorf("a policy with no subjects would make Caddy manage every hostname; got %v", policies)
		}
	}
}

var testRelay = &HostRelay{
	Host:       "host.docker.internal",
	Dial:       "unix//run/caddy_shack/relay.sock",
	PortHeader: "X-Caddy-Shack-Port",
}

func TestBuildZeroOptionsEmitsNoHeaders(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web":  {Domain: "web.test", Upstream: "host.docker.internal:3000"},
			"app":  {Domain: "app.test", Upstream: "app:80"},
			"bare": {Domain: "bare.test", Upstream: "127.0.0.1:4000"},
		},
	}
	if strings.Contains(string(Build(s, Options{})), `"headers"`) {
		t.Error("Build must not emit a headers block")
	}
}

func TestBuildWithHostRelayReroutesOnlyHostMode(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web": {Domain: "web.test", Upstream: "host.docker.internal:3000"},
			"app": {Domain: "app.test", Upstream: "app:80"},
		},
	}
	got := Build(s, Options{HostRelay: testRelay})
	var doc struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match  []struct{ Host []string }
						Handle []struct {
							Upstreams []struct{ Dial string }
							Headers   *struct {
								Request struct {
									Set map[string][]string
								}
							}
						}
					}
				}
			}
		}
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	routes := doc.Apps.HTTP.Servers["srv0"].Routes
	if len(routes) != 4 {
		t.Fatalf("routes: got %d want 4", len(routes))
	}
	for _, r := range routes {
		host := r.Match[0].Host[0]
		h := r.Handle[0]
		switch host {
		case "web.test", "*.web.test":
			if h.Upstreams[0].Dial != testRelay.Dial {
				t.Errorf("%s dial: %q", host, h.Upstreams[0].Dial)
			}
			if h.Headers == nil || strings.Join(h.Headers.Request.Set["X-Caddy-Shack-Port"], ",") != "3000" {
				t.Errorf("%s: missing port header: %+v", host, h.Headers)
			}
		case "app.test", "*.app.test":
			if h.Upstreams[0].Dial != "app:80" || h.Headers != nil {
				t.Errorf("%s: container-mode route changed: %+v", host, h)
			}
		default:
			t.Errorf("unexpected route %s", host)
		}
	}
}

// internalPolicySubjects extracts the internal issuer's automation policy
// subjects from a Build result, or nil if there is no policy at all.
func internalPolicySubjects(t *testing.T, got []byte) []string {
	t.Helper()
	var cfg map[string]any
	if err := json.Unmarshal(got, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tlsApp := cfg["apps"].(map[string]any)["tls"].(map[string]any)
	automation, present := tlsApp["automation"]
	if !present {
		return nil
	}
	policiesAny, ok := automation.(map[string]any)["policies"].([]any)
	if !ok || len(policiesAny) == 0 {
		return nil
	}
	subjectsAny := policiesAny[0].(map[string]any)["subjects"].([]any)
	subjects := make([]string, 0, len(subjectsAny))
	for _, s := range subjectsAny {
		subjects = append(subjects, s.(string))
	}
	return subjects
}

func containsSubject(subjects []string, want string) bool {
	for _, s := range subjects {
		if s == want {
			return true
		}
	}
	return false
}

// TestBuildParentWildcardCoversSubdomainButNotItsOwnWildcard pins the fix for
// a defect found in review: a wildcard certificate (*.alpha.arrix.dev) covers
// a plain subdomain registered as its own backend (vite.alpha.arrix.dev), but
// does not cover that subdomain's own wildcard (*.vite.alpha.arrix.dev),
// because a Caddy `*` matches exactly one label. Both alpha.arrix.dev and
// vite.alpha.arrix.dev must be withheld from the internal issuer, while
// *.vite.alpha.arrix.dev must still be minted internally, and the unrelated
// .test backend must be untouched.
func TestBuildParentWildcardCoversSubdomainButNotItsOwnWildcard(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
			"root":  {Domain: "alpha.arrix.dev", Upstream: "host.docker.internal:4000"},
			"vite":  {Domain: "vite.alpha.arrix.dev", Upstream: "host.docker.internal:5173"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, Options{Certs: certs})

	subjects := internalPolicySubjects(t, got)

	for _, notWant := range []string{"alpha.arrix.dev", "*.alpha.arrix.dev", "vite.alpha.arrix.dev"} {
		if containsSubject(subjects, notWant) {
			t.Errorf("internal policy must not claim %q, it has a loaded certificate; got %v", notWant, subjects)
		}
	}
	if !containsSubject(subjects, "*.vite.alpha.arrix.dev") {
		t.Errorf("internal policy must still mint *.vite.alpha.arrix.dev, the parent wildcard does not cover it; got %v", subjects)
	}
	if !containsSubject(subjects, "hello.test") {
		t.Errorf("internal policy lost the unrelated hello.test backend; got %v", subjects)
	}
}

// TestBuildParentWildcardDoesNotCoverTwoLevelsDown pins that a single-label
// wildcard cert never covers a two-label-deep subdomain: Caddy's `*` matches
// exactly one label, so *.alpha.arrix.dev cannot serve a.b.alpha.arrix.dev.
func TestBuildParentWildcardDoesNotCoverTwoLevelsDown(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"hello": {Domain: "hello.test", Upstream: "host.docker.internal:3000"},
			"deep":  {Domain: "a.b.alpha.arrix.dev", Upstream: "host.docker.internal:6000"},
		},
		Certs: map[string]state.Cert{},
	}
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "cert", KeyPEM: "key",
	}}
	got := Build(s, Options{Certs: certs})

	subjects := internalPolicySubjects(t, got)

	if !containsSubject(subjects, "a.b.alpha.arrix.dev") {
		t.Errorf("internal policy must still mint a.b.alpha.arrix.dev, *.alpha.arrix.dev does not reach two labels down; got %v", subjects)
	}
	if !containsSubject(subjects, "hello.test") {
		t.Errorf("internal policy lost the unrelated hello.test backend; got %v", subjects)
	}
}

func lanState() *state.State {
	return &state.State{
		SchemaVersion: 2,
		Backends: map[string]state.Backend{
			"gamma": {Domain: "gamma.arrix.test", Upstream: "gamma:3000", LANDomain: "gamma.arrix.home.lan"},
			"web":   {Domain: "web.test", Upstream: "host.docker.internal:4000"},
		},
		LAN: &state.LAN{Enabled: true, IP: "192.168.1.20", Mode: state.LANModeDiscovered, Suffix: "arrix.home.lan"},
	}
}

func TestBuildLANEnabledGolden(t *testing.T) {
	assertJSONMatchesGolden(t, Build(lanState(), Options{}), "lan_enabled.json")
}

func TestBuildLANEnabledNoNamesGolden(t *testing.T) {
	s := lanState()
	g := s.Backends["gamma"]
	g.LANDomain = ""
	s.Backends["gamma"] = g
	assertJSONMatchesGolden(t, Build(s, Options{}), "lan_enabled_no_lan_names.json")
}

func TestBuildLANDisabledIgnoresLANDomain(t *testing.T) {
	disabled := lanState()
	disabled.LAN = &state.LAN{Enabled: false}

	plain := lanState()
	plain.LAN = nil
	g := plain.Backends["gamma"]
	g.LANDomain = ""
	plain.Backends["gamma"] = g

	for _, opts := range []Options{{}, {HostRelay: testRelay}} {
		if string(Build(disabled, opts)) != string(Build(plain, opts)) {
			t.Errorf("LAN disabled must render as if LANDomain were unset (opts %+v)", opts)
		}
	}
}

// lanDoc decodes just enough of a rendered config to inspect the LAN servers.
type lanDoc struct {
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Listen           []string `json:"listen"`
				ListenerWrappers []struct {
					Wrapper string `json:"wrapper"`
				} `json:"listener_wrappers"`
				Routes []struct {
					Match []struct {
						Host []string `json:"host"`
					} `json:"match"`
					Handle []struct {
						Handler   string `json:"handler"`
						Upstreams []struct {
							Dial string `json:"dial"`
						} `json:"upstreams"`
					} `json:"handle"`
				} `json:"routes"`
				TLSConnectionPolicies []struct {
					Match struct {
						SNI []string `json:"sni"`
					} `json:"match"`
				} `json:"tls_connection_policies"`
			} `json:"servers"`
		} `json:"http"`
	} `json:"apps"`
}

func decodeLAN(t *testing.T, got []byte) lanDoc {
	t.Helper()
	var doc lanDoc
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, name := range []string{"srv-lan", "lan-http"} {
		if _, ok := doc.Apps.HTTP.Servers[name]; !ok {
			t.Fatalf("server %s missing", name)
		}
	}
	return doc
}

func TestSrvLanNeverRoutesTestNames(t *testing.T) {
	doc := decodeLAN(t, Build(lanState(), Options{}))
	for _, name := range []string{"srv-lan", "lan-http"} {
		srv := doc.Apps.HTTP.Servers[name]
		for _, r := range srv.Routes {
			for _, m := range r.Match {
				for _, h := range m.Host {
					if h == "test" || strings.HasSuffix(h, ".test") {
						t.Errorf("%s routes .test host %q", name, h)
					}
				}
			}
		}
		for _, p := range srv.TLSConnectionPolicies {
			for _, sni := range p.Match.SNI {
				if strings.HasSuffix(sni, ".test") {
					t.Errorf("%s TLS policy matches .test SNI %q", name, sni)
				}
			}
		}
	}
}

func TestListenerWrapperOrder(t *testing.T) {
	doc := decodeLAN(t, Build(lanState(), Options{}))
	var got []string
	for _, w := range doc.Apps.HTTP.Servers["srv-lan"].ListenerWrappers {
		got = append(got, w.Wrapper)
	}
	if strings.Join(got, ",") != "proxy_protocol,tls" {
		t.Errorf("srv-lan wrappers: got %v want [proxy_protocol tls]", got)
	}
}

func TestBuildLANListenPorts(t *testing.T) {
	doc := decodeLAN(t, Build(lanState(), Options{}))
	if got := doc.Apps.HTTP.Servers["srv-lan"].Listen; strings.Join(got, ",") != ":"+strconv.Itoa(LANHTTPSListenPort) {
		t.Errorf("srv-lan listen: %v", got)
	}
	if got := doc.Apps.HTTP.Servers["lan-http"].Listen; strings.Join(got, ",") != ":"+strconv.Itoa(LANHTTPListenPort) {
		t.Errorf("lan-http listen: %v", got)
	}
}

// TestSrvLanUsesHostRelay pins that srv-lan routes share srv0's proxy
// handler, so a host-mode backend exposed on the LAN is relayed on native
// Linux exactly as it is on loopback.
func TestSrvLanUsesHostRelay(t *testing.T) {
	s := lanState()
	w := s.Backends["web"]
	w.LANDomain = "web.arrix.home.lan"
	s.Backends["web"] = w
	doc := decodeLAN(t, Build(s, Options{HostRelay: testRelay}))
	seen := false
	for _, r := range doc.Apps.HTTP.Servers["srv-lan"].Routes {
		if len(r.Match) == 0 {
			continue
		}
		host := r.Match[0].Host[0]
		if host == "web.arrix.home.lan" || host == "*.web.arrix.home.lan" {
			seen = true
			if d := r.Handle[0].Upstreams[0].Dial; d != testRelay.Dial {
				t.Errorf("%s dial: got %q want relay", host, d)
			}
		}
	}
	if !seen {
		t.Fatal("no srv-lan route for web.arrix.home.lan")
	}
}

// The LAN forwarder carries TCP only, so the LAN servers must not advertise
// HTTP/3 on a UDP port nobody publishes. srv0 keeps Caddy's defaults.
func TestBuildLANServersDisableHTTP3(t *testing.T) {
	var doc struct {
		Apps struct {
			HTTP struct {
				Servers map[string]map[string]any `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(Build(lanState(), Options{}), &doc); err != nil {
		t.Fatal(err)
	}
	servers := doc.Apps.HTTP.Servers
	for name, want := range map[string][]any{"srv-lan": {"h1", "h2"}, "lan-http": {"h1"}} {
		if got := servers[name]["protocols"]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s protocols = %v, want %v", name, got, want)
		}
	}
	if _, ok := servers["srv0"]["protocols"]; ok {
		t.Error("srv0 must not set protocols")
	}
}

func TestBuildWithCertsKeepsLANServersIntact(t *testing.T) {
	certs := []LoadedCert{{
		Subjects: []string{"alpha.arrix.dev", "*.alpha.arrix.dev"},
		CertPEM:  "CERT",
		KeyPEM:   "KEY",
	}}
	plain := decodeLAN(t, Build(lanState(), Options{}))
	withCerts := decodeLAN(t, Build(lanState(), Options{Certs: certs}))
	if !reflect.DeepEqual(plain, withCerts) {
		t.Errorf("loading a certificate for an unrelated domain changed the LAN servers\nwithout: %+v\nwith:    %+v", plain, withCerts)
	}
}

func TestBuildWithheldLANNamesCoveredByCert(t *testing.T) {
	certs := []LoadedCert{{
		Subjects: []string{"arrix.home.lan", "*.arrix.home.lan"},
		CertPEM:  "CERT",
		KeyPEM:   "KEY",
	}}
	subjects := internalPolicySubjects(t, Build(lanState(), Options{Certs: certs}))

	if containsSubject(subjects, "gamma.arrix.home.lan") {
		t.Errorf("a LAN name covered by a loaded wildcard must not go to the internal issuer; got %v", subjects)
	}
	if !containsSubject(subjects, "*.gamma.arrix.home.lan") {
		t.Errorf("*.gamma.arrix.home.lan is two labels below the cert's wildcard and still needs the internal issuer; got %v", subjects)
	}
	if !containsSubject(subjects, "gamma.arrix.test") {
		t.Errorf("internal policy lost the .test name; got %v", subjects)
	}
}

func TestBuildOmitsPolicyWhenOnlyLANNamesAllCovered(t *testing.T) {
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends:      map[string]state.Backend{},
		LAN:           &state.LAN{Enabled: true, IP: "192.168.1.20", Mode: state.LANModeDiscovered, Suffix: "arrix.dev"},
	}
	// An empty subjects list means "every hostname" to Caddy, so with every
	// name covered there must be no internal policy at all.
	s.Backends["gamma"] = state.Backend{Domain: "gamma.arrix.dev", Upstream: "gamma:3000", LANDomain: "lan.arrix.dev"}
	certs := []LoadedCert{
		{Subjects: []string{"gamma.arrix.dev", "*.gamma.arrix.dev"}, CertPEM: "A", KeyPEM: "A"},
		{Subjects: []string{"lan.arrix.dev", "*.lan.arrix.dev"}, CertPEM: "B", KeyPEM: "B"},
	}
	if subjects := internalPolicySubjects(t, Build(s, Options{Certs: certs})); len(subjects) != 0 {
		t.Errorf("every name is covered, so no internal policy should exist; got %v", subjects)
	}
}

// TestBuildAdminEnforcesOrigin pins the whole rendered admin object: bound
// to every interface (so the host's published port reaches it) with
// enforce_origin on, so a browser page that DNS-rebinds to 127.0.0.1 (and
// cannot forge Origin) is refused.
func TestBuildAdminEnforcesOrigin(t *testing.T) {
	got := Build(&state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}, Options{})
	var doc struct {
		Admin json.RawMessage `json:"admin"`
	}
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc.Admin); err != nil {
		t.Fatalf("compact: %v", err)
	}
	want := `{"listen":"0.0.0.0:2019","enforce_origin":true,"origins":["127.0.0.1:2019","localhost:2019"]}`
	if compact.String() != want {
		t.Errorf("admin:\n got  %s\n want %s", compact.String(), want)
	}
}
