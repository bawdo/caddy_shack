package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// fixture wires an App against a temp dir + httptest Caddy stub + Fake docker.
//
// The stub models the parts of Caddy's admin API CaddyShack uses: POST /load
// answers caddyStatus and, on 2xx, becomes the live config; GET /config/
// returns the live config. It starts as the bare admin-only config Caddy
// boots with, so setLive(bareCaddyConfig) models an out-of-band restart.
// caddyHits and lastBody count and capture POST /load only.
type fixture struct {
	app       *App
	statePath string
	caddyHits int
	lastBody  []byte
	live      []byte
	mu        sync.Mutex
	caddySrv  *httptest.Server
}

// bareCaddyConfig is what GET /config/ returns from a Caddy that booted
// from the admin-only Caddyfile: no http app, no tls app, no routes.
const bareCaddyConfig = `{"admin":{"listen":"0.0.0.0:2019"}}`

func newFixture(t *testing.T, caddyStatus int) *fixture {
	t.Helper()
	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning
	return newFixtureWith(t, caddyStatus, fakeDock)
}

// newFixtureWith is newFixture around a caller-prepared Fake docker.
func newFixtureWith(t *testing.T, caddyStatus int, fakeDock *docker.Fake) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		statePath: filepath.Join(dir, "state.json"),
		live:      []byte(bareCaddyConfig),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Method == http.MethodGet {
			_, _ = w.Write(f.live)
			return
		}
		f.caddyHits++
		f.lastBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(caddyStatus)
		if caddyStatus >= 400 {
			_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
			return
		}
		f.live = f.lastBody
	}))
	f.caddySrv = srv
	t.Cleanup(srv.Close)

	f.app = NewWithPaths(
		Paths{
			StatePath: f.statePath,
			LockPath:  f.statePath + ".lock",
		},
		caddyapi.NewClient(srv.URL, time.Second),
		fakeDock,
	)
	return f
}

// setLive replaces the stub's live config, e.g. with bareCaddyConfig to
// model Caddy restarting outside caddy_shack.
func (f *fixture) setLive(cfg string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.live = []byte(cfg)
}

// syncLive makes the stub's live config exactly what the registry on disk
// renders, i.e. a Caddy that is in sync.
func (f *fixture) syncLive(t *testing.T) {
	t.Helper()
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	f.setLive(string(f.app.buildConfig(s)))
}

// loads returns how many POST /load requests the stub has received.
func (f *fixture) loads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.caddyHits
}

func TestRegisterNewBackendPersistsAndPushesConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name: "myapp",
		Port: 3000,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b, ok := s.Backends["myapp"]
	if !ok {
		t.Fatal("backend not persisted")
	}
	if b.Domain != "myapp.test" {
		t.Errorf("Domain: %q", b.Domain)
	}
	if b.Upstream != "host.docker.internal:3000" {
		t.Errorf("Upstream: %q", b.Upstream)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("Caddy not given the route: %s", f.lastBody)
	}
}

func TestRegisterCustomDomain(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{
		Name:   "myapp",
		Port:   3000,
		Domain: "custom.test",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["myapp"].Domain != "custom.test" {
		t.Errorf("Domain: %s", s.Backends["myapp"].Domain)
	}
}

func TestRegisterSameFieldsIsNoOp(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	in := RegisterInput{Name: "myapp", Port: 3000}
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("Register1: %v", err)
	}
	hits := f.caddyHits
	if err := f.app.Register(context.Background(), in); err != nil {
		t.Fatalf("Register2: %v", err)
	}
	if f.caddyHits != hits {
		t.Errorf("Caddy hit again on identical re-register: %d -> %d", hits, f.caddyHits)
	}
}

func TestRegisterUpdatesOnFieldChange(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Port: 3000}); err != nil {
		t.Fatalf("Register1: %v", err)
	}
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Port: 9999}); err != nil {
		t.Fatalf("Register2: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["myapp"].Upstream != "host.docker.internal:9999" {
		t.Errorf("Upstream not updated: %s", s.Backends["myapp"].Upstream)
	}
}

func TestRegisterDomainConflictReturnsErrDomainConflict(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 3000}); err != nil {
		t.Fatalf("foo: %v", err)
	}
	err := f.app.Register(context.Background(), RegisterInput{Name: "bar", Domain: "foo.test", Port: 4000})
	if !errors.Is(err, ErrDomainConflict) {
		t.Errorf("expected ErrDomainConflict, got %v", err)
	}
	// State must not have been mutated.
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["bar"]; ok {
		t.Error("bar persisted despite conflict")
	}
}

func TestRegisterInvalidNameReturnsErrInvalidArgs(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "Bad Name!", Port: 3000})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestRegisterInvalidPortReturnsErrInvalidArgs(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	for _, p := range []int{0, -1, 65536, 100000} {
		err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: p})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("port %d: expected ErrInvalidArgs, got %v", p, err)
		}
	}
}

func TestRegisterCaddyRejectionDoesNotPersistState(t *testing.T) {
	f := newFixture(t, http.StatusBadRequest)
	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 3000})
	if err == nil {
		t.Fatal("expected error from caddy rejection")
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; ok {
		t.Error("state persisted despite Caddy rejection")
	}
}

func TestRegisterReturnsErrCaddyNotRunningWhenContainerStopped(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.caddySrv.Close() // simulate unreachable admin API
	// Also reflect container state so Register detects it before HTTP.
	f.app.docker.(*docker.Fake).Containers[CaddyContainerName] = docker.StateStopped

	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 3000})
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("expected ErrCaddyNotRunning, got %v", err)
	}
}

// Smoke check: the body POSTed to Caddy is the JSON our builder produces.
func TestRegisterPostsBuiltConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Port: 3000}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(f.lastBody, &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if _, ok := got["apps"]; !ok {
		t.Errorf("body missing 'apps' key: %s", f.lastBody)
	}
}

func TestDeregisterRemovesBackendAndPushesConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 3000}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	res, err := f.app.Deregister(context.Background(), "foo", false)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !res.Removed {
		t.Error("Deregister should report removed=true for an existing backend")
	}

	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; ok {
		t.Error("backend still present in state")
	}
	// Caddy should have been called twice: register + deregister.
	if f.caddyHits != 2 {
		t.Errorf("caddy hits: got %d want 2", f.caddyHits)
	}
}

func TestDeregisterUnknownNameIsNoOp(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	hits := f.caddyHits
	res, err := f.app.Deregister(context.Background(), "unknown", false)
	if err != nil {
		t.Errorf("expected nil err, got %v", err)
	}
	if res.Removed {
		t.Error("Deregister should report removed=false for an unknown backend")
	}
	if f.caddyHits != hits {
		t.Error("Caddy was called for unknown name")
	}
}

func TestDeregisterCaddyRejectionLeavesStateIntact(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 3000}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Swap server to return 400 from now on.
	f.caddySrv.Close()
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("{}"))
	}))
	defer rejecting.Close()
	f.app = NewWithPaths(
		Paths{StatePath: f.statePath, LockPath: f.statePath + ".lock"},
		caddyapi.NewClient(rejecting.URL, time.Second),
		f.app.docker,
	)

	res, err := f.app.Deregister(context.Background(), "foo", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Removed {
		t.Error("Deregister should report removed=false when Caddy rejects")
	}
	if !strings.Contains(err.Error(), "rejected") && !strings.Contains(err.Error(), "stub") {
		t.Logf("error: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if _, ok := s.Backends["foo"]; !ok {
		t.Error("state lost despite Caddy rejection")
	}
}

func TestRegisterAcceptsDottedSubdomainName(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000})
	if err != nil {
		t.Fatalf("Register dotted name: %v", err)
	}
	if !strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("Caddy not given the dotted route: %s", f.lastBody)
	}
}

func TestRegisterRejectsMalformedDottedName(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	for _, bad := range []string{"foo..bar", ".foo", "foo.", "-bad.foo", "foo.-bad"} {
		err := f.app.Register(context.Background(), RegisterInput{Name: bad, Port: 3000})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("name %q: want ErrInvalidArgs, got %v", bad, err)
		}
	}
}

// seedParentAndChild registers a parent and a subdomain override.
func seedParentAndChild(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Port: 2000}); err != nil {
		t.Fatalf("seed foo: %v", err)
	}
	if err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000}); err != nil {
		t.Fatalf("seed bar.foo: %v", err)
	}
}

func TestDeregisterParentLeavesChildByDefault(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedParentAndChild(t, f)

	res, err := f.app.Deregister(context.Background(), "foo", false)
	if err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if !res.Removed {
		t.Error("parent should be removed")
	}
	if len(res.ChildrenLeft) != 1 || res.ChildrenLeft[0] != "bar.foo" {
		t.Errorf("ChildrenLeft: got %v want [bar.foo]", res.ChildrenLeft)
	}
	if res.ParentDomain != "foo.test" {
		t.Errorf("ParentDomain: got %q want foo.test", res.ParentDomain)
	}
	if !strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("child route should survive: %s", f.lastBody)
	}
	if strings.Contains(string(f.lastBody), `"foo.test"`) {
		t.Errorf("parent exact route should be gone: %s", f.lastBody)
	}
}

func TestDeregisterCascadeRemovesChildren(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedParentAndChild(t, f)

	res, err := f.app.Deregister(context.Background(), "foo", true)
	if err != nil {
		t.Fatalf("Deregister cascade: %v", err)
	}
	if !res.Removed {
		t.Error("parent should be removed")
	}
	if len(res.Cascaded) != 1 || res.Cascaded[0] != "bar.foo" {
		t.Errorf("Cascaded: got %v want [bar.foo]", res.Cascaded)
	}
	if strings.Contains(string(f.lastBody), "bar.foo.test") {
		t.Errorf("child route should be gone under cascade: %s", f.lastBody)
	}
}

func TestDeregisterCascadeOnAbsentParentStillRemovesChildren(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "bar.foo", Port: 3000}); err != nil {
		t.Fatalf("seed bar.foo: %v", err)
	}
	res, err := f.app.Deregister(context.Background(), "foo", true)
	if err != nil {
		t.Fatalf("Deregister cascade: %v", err)
	}
	if res.Removed {
		t.Error("absent parent should report Removed=false")
	}
	if len(res.Cascaded) != 1 || res.Cascaded[0] != "bar.foo" {
		t.Errorf("Cascaded: got %v want [bar.foo]", res.Cascaded)
	}
}

// lanSeed saves a registry with LAN mode on (suffix arrix.home.lan) plus
// the given backends.
func lanSeed(t *testing.T, f *fixture, enabled bool, backends map[string]state.Backend) {
	t.Helper()
	s := &state.State{
		Backends: backends,
		LAN:      &state.LAN{Enabled: enabled, IP: "192.168.1.12", Mode: state.LANModePinned, Suffix: "arrix.home.lan"},
	}
	if err := state.Save(f.statePath, s); err != nil {
		t.Fatal(err)
	}
}

func lanOf(t *testing.T, f *fixture, name string) string {
	t.Helper()
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	return s.Backends[name].LANDomain
}

func TestRegisterLANUsesSuffix(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, LAN: true}); err != nil {
		t.Fatal(err)
	}
	if got := lanOf(t, f, "gamma"); got != "gamma.arrix.home.lan" {
		t.Errorf("LANDomain = %q", got)
	}
}

func TestRegisterLANDottedName(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "bar.gamma", Port: 3000, LAN: true}); err != nil {
		t.Fatal(err)
	}
	if got := lanOf(t, f, "bar.gamma"); got != "bar.gamma.arrix.home.lan" {
		t.Errorf("LANDomain = %q", got)
	}
}

func TestRegisterLANDomainOverride(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, LANDomain: "app.example.lan"}); err != nil {
		t.Fatal(err)
	}
	if got := lanOf(t, f, "gamma"); got != "app.example.lan" {
		t.Errorf("LANDomain = %q", got)
	}
}

func TestRegisterLANWhenDisabled(t *testing.T) {
	for _, in := range []RegisterInput{
		{Name: "gamma", Port: 3000, LAN: true},
		{Name: "gamma", Port: 3000, LANDomain: "g.example.lan"},
	} {
		f := newFixture(t, http.StatusOK)
		lanSeed(t, f, false, map[string]state.Backend{
			"other": {Domain: "other.test", Upstream: "host.docker.internal:1", LANDomain: "other.arrix.home.lan"},
		})
		err := f.app.Register(context.Background(), in)
		if !errors.Is(err, ErrLANDisabled) || ExitCode(err) != 10 {
			t.Fatalf("want ErrLANDisabled exit 10, got %v", err)
		}
		if got := lanOf(t, f, "other"); got != "other.arrix.home.lan" {
			t.Errorf("other LANDomain touched: %q", got)
		}
		if f.loads() != 0 {
			t.Error("caddy was loaded")
		}
	}
}

func TestRegisterLANDomainRejectsTest(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	for _, bad := range []string{"foo.test", "test"} {
		err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, LANDomain: bad})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%q: want ErrInvalidArgs, got %v", bad, err)
		}
	}
}

func TestRegisterLANConflictAcrossSpaces(t *testing.T) {
	const lan = "gamma.arrix.home.lan"
	ctx := context.Background()

	// Existing backend owns the name as its Domain; new one wants it as LAN.
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, map[string]state.Backend{"a": {Domain: lan, Upstream: "host.docker.internal:1"}})
	err := f.app.Register(ctx, RegisterInput{Name: "b", Port: 2, LANDomain: lan})
	if !errors.Is(err, ErrDomainConflict) {
		t.Errorf("lan vs domain: got %v", err)
	}

	// Existing backend owns it as LANDomain; new one wants it as Domain.
	f = newFixture(t, http.StatusOK)
	lanSeed(t, f, true, map[string]state.Backend{"a": {Domain: "a.test", Upstream: "host.docker.internal:1", LANDomain: lan}})
	err = f.app.Register(ctx, RegisterInput{Name: "b", Port: 2, Domain: lan})
	if !errors.Is(err, ErrDomainConflict) {
		t.Errorf("domain vs lan: got %v", err)
	}

	// Duplicate LANDomain.
	err = f.app.Register(ctx, RegisterInput{Name: "c", Port: 3, LANDomain: lan})
	if !errors.Is(err, ErrDomainConflict) {
		t.Errorf("lan vs lan: got %v", err)
	}

	// Own row is excluded.
	if err := f.app.Register(ctx, RegisterInput{Name: "a", Port: 1, LANDomain: lan}); err != nil {
		t.Errorf("own row conflicted: %v", err)
	}
}

func TestRegisterWithoutLANClearsIt(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	ctx := context.Background()
	if err := f.app.Register(ctx, RegisterInput{Name: "gamma", Port: 3000, LAN: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Register(ctx, RegisterInput{Name: "gamma", Port: 3000}); err != nil {
		t.Fatal(err)
	}
	if got := lanOf(t, f, "gamma"); got != "" {
		t.Errorf("LANDomain = %q, want empty", got)
	}
}

func TestRegisterBothFlagsInvalid(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, LAN: true, LANDomain: "x.lan"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("got %v", err)
	}
}

func TestRegisterRejectsOwnDomainAsOwnLANDomain(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, true, nil)
	err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, Domain: "gamma.example.lan", LANDomain: "gamma.example.lan"})
	if !errors.Is(err, ErrDomainConflict) || ExitCode(err) != 3 {
		t.Fatalf("want ErrDomainConflict exit 3, got %v", err)
	}
	if f.loads() != 0 {
		t.Error("caddy was loaded")
	}
}

// --lan builds the name from the backend name and the suffix, so it must be
// validated too: a long dotted name can push it past 253 characters.
func TestRegisterLANValidatesResolvedName(t *testing.T) {
	label := strings.Repeat("a", 31)
	long := strings.Join([]string{label, label, label, label, label, label, label, label}, ".")
	for _, in := range []RegisterInput{
		{Name: long, Port: 3000, LAN: true},
		{Name: "gamma-", Port: 3000, LAN: true},
		{Name: "gamma", Port: 3000, LANDomain: long + ".arrix.home.lan"},
	} {
		f := newFixture(t, http.StatusOK)
		lanSeed(t, f, true, nil)
		err := f.app.Register(context.Background(), in)
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%+v: want ErrInvalidArgs, got %v", in, err)
		}
		if f.loads() != 0 {
			t.Errorf("%+v: caddy was loaded", in)
		}
	}
}

func TestRegisterLANDisabledMessageSaysRemedyOnce(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	lanSeed(t, f, false, nil)
	err := f.app.Register(context.Background(), RegisterInput{Name: "gamma", Port: 3000, LAN: true})
	if n := strings.Count(err.Error(), "caddy_shack lan enable"); n != 1 {
		t.Errorf("remedy appears %d times: %q", n, err)
	}
}
