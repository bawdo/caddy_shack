package caddyapi

import (
	"encoding/json"
	"os"
	"path/filepath"
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
