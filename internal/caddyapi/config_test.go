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
	got := Build(s, nil)
	assertJSONMatchesGolden(t, got, "empty.json")
}

func TestBuildConfigOneBackend(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "127.0.0.1:3000"},
		},
	}
	got := Build(s, nil)
	assertJSONMatchesGolden(t, got, "one_backend.json")
}

// TestBuildPreservesAdminBinding pins down that every rendered config keeps
// admin bound to 0.0.0.0:2019. Without this, POST /load reverts admin to
// caddy's default localhost:2019 and the next register/deregister can't
// reach the container's admin API from the host. Discovered by smoke test.
func TestBuildPreservesAdminBinding(t *testing.T) {
	got := Build(&state.State{SchemaVersion: 1, Backends: map[string]state.Backend{}}, nil)
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
	got := Build(s, nil)
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
	got := Build(s, nil)
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
	got := Build(s, nil)

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
	got := Build(s, nil)
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
	got := Build(s, nil)

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
	got := Build(s, certs)

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
	got := Build(s, certs)

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
	got := Build(s, certs)

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
	got := Build(s, certs)

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
	got := Build(s, certs)

	subjects := internalPolicySubjects(t, got)

	if !containsSubject(subjects, "a.b.alpha.arrix.dev") {
		t.Errorf("internal policy must still mint a.b.alpha.arrix.dev, *.alpha.arrix.dev does not reach two labels down; got %v", subjects)
	}
	if !containsSubject(subjects, "hello.test") {
		t.Errorf("internal policy lost the unrelated hello.test backend; got %v", subjects)
	}
}
