package caddyapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigEmptyState(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "empty.json")
}

func TestBuildConfigOneBackend(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "one_backend.json")
}

// TestBuildPreservesAdminBinding pins down that every rendered config keeps
// admin bound to 0.0.0.0:2019. Without this, POST /load reverts admin to
// caddy's default localhost:2019 and the next register/deregister can't
// reach the container's admin API from the host. Discovered by smoke test.
func TestBuildPreservesAdminBinding(t *testing.T) {
	got := Build(&state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}})
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
	got := Build(s)
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
	got := Build(s)
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
	got := Build(s)

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
	got := Build(s)
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

var testRelay = &HostRelay{
	Host:       "host.docker.internal",
	Dial:       "unix//run/caddy_shack/relay.sock",
	PortHeader: "X-Caddy-Shack-Port",
}

func TestBuildWithZeroOptionsIsBuild(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"web":  {Domain: "web.test", Upstream: "host.docker.internal:3000"},
			"app":  {Domain: "app.test", Upstream: "app:80"},
			"bare": {Domain: "bare.test", Upstream: "127.0.0.1:4000"},
		},
	}
	if string(BuildWith(s, Options{})) != string(Build(s)) {
		t.Error("BuildWith(s, Options{}) must be byte-identical to Build(s)")
	}
	if strings.Contains(string(Build(s)), `"headers"`) {
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
	got := BuildWith(s, Options{HostRelay: testRelay})
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
	assertJSONMatchesGolden(t, Build(lanState()), "lan_enabled.json")
}

func TestBuildLANEnabledNoNamesGolden(t *testing.T) {
	s := lanState()
	g := s.Backends["gamma"]
	g.LANDomain = ""
	s.Backends["gamma"] = g
	assertJSONMatchesGolden(t, Build(s), "lan_enabled_no_lan_names.json")
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
		if string(BuildWith(disabled, opts)) != string(BuildWith(plain, opts)) {
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
	doc := decodeLAN(t, Build(lanState()))
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
	doc := decodeLAN(t, Build(lanState()))
	var got []string
	for _, w := range doc.Apps.HTTP.Servers["srv-lan"].ListenerWrappers {
		got = append(got, w.Wrapper)
	}
	if strings.Join(got, ",") != "proxy_protocol,tls" {
		t.Errorf("srv-lan wrappers: got %v want [proxy_protocol tls]", got)
	}
}

func TestBuildLANListenPorts(t *testing.T) {
	doc := decodeLAN(t, Build(lanState()))
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
	doc := decodeLAN(t, BuildWith(s, Options{HostRelay: testRelay}))
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
