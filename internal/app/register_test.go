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
type fixture struct {
	app       *App
	statePath string
	caddyHits int
	lastBody  []byte
	mu        sync.Mutex
	caddySrv  *httptest.Server
}

func newFixture(t *testing.T, caddyStatus int) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{
		statePath: filepath.Join(dir, "state.json"),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.caddyHits++
		f.lastBody, _ = io.ReadAll(r.Body)
		f.mu.Unlock()
		w.WriteHeader(caddyStatus)
		if caddyStatus >= 400 {
			_, _ = w.Write([]byte(`{"error":"stub rejection"}`))
		}
	}))
	f.caddySrv = srv
	t.Cleanup(srv.Close)

	fakeDock := docker.NewFake()
	fakeDock.Containers[CaddyContainerName] = docker.StateRunning

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

func TestRegisterNewBackendPersistsAndPushesConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name:     "myapp",
		Upstream: "127.0.0.1:3000",
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
	if b.Upstream != "127.0.0.1:3000" {
		t.Errorf("Upstream: %q", b.Upstream)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("Caddy not given the route: %s", f.lastBody)
	}
}

func TestRegisterCustomDomain(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{
		Name:     "myapp",
		Upstream: "127.0.0.1:3000",
		Domain:   "custom.test",
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
	in := RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}
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
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("Register1: %v", err)
	}
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:9999"}); err != nil {
		t.Fatalf("Register2: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["myapp"].Upstream != "127.0.0.1:9999" {
		t.Errorf("Upstream not updated: %s", s.Backends["myapp"].Upstream)
	}
}

func TestRegisterDomainConflictReturnsErrDomainConflict(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"}); err != nil {
		t.Fatalf("foo: %v", err)
	}
	err := f.app.Register(context.Background(), RegisterInput{Name: "bar", Domain: "foo.test", Upstream: "127.0.0.1:4000"})
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
	err := f.app.Register(context.Background(), RegisterInput{Name: "Bad Name!", Upstream: "127.0.0.1:3000"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestRegisterInvalidUpstreamReturnsErrInvalidArgs(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "not a host port"})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs, got %v", err)
	}
}

func TestRegisterCaddyRejectionDoesNotPersistState(t *testing.T) {
	f := newFixture(t, http.StatusBadRequest)
	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"})
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

	err := f.app.Register(context.Background(), RegisterInput{Name: "foo", Upstream: "127.0.0.1:3000"})
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Errorf("expected ErrCaddyNotRunning, got %v", err)
	}
}

// Smoke check: the body POSTed to Caddy is the JSON our builder produces.
func TestRegisterPostsBuiltConfig(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "myapp", Upstream: "127.0.0.1:3000"}); err != nil {
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
