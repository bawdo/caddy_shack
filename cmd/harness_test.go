package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/acme"
	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

// fakeCaddy is a stand-in for Caddy's admin API that keeps whatever config
// was last loaded and hands it back from GET /config/, the way the real one
// does. That makes drift observable: a test can wipe the live config and
// watch status, sync and restart react to it.
type fakeCaddy struct {
	mu     sync.Mutex
	config []byte
	loads  int
	// rejectLoad, when non-zero, is the status every POST /load answers
	// with, modelling Caddy refusing a config.
	rejectLoad int
	// ca is what GET /pki/ca/local returns; nil answers 404.
	ca *caddyapi.CAInfo
}

func (c *fakeCaddy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/config/":
		_, _ = w.Write(c.config)
	case r.Method == http.MethodPost && r.URL.Path == "/load":
		if c.rejectLoad != 0 {
			w.WriteHeader(c.rejectLoad)
			_, _ = w.Write([]byte(`{"error":"loading config: rejected by test"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		c.config = body
		c.loads++
	case r.Method == http.MethodGet && r.URL.Path == "/pki/ca/local" && c.ca != nil:
		_ = json.NewEncoder(w).Encode(c.ca)
	default:
		http.NotFound(w, r)
	}
}

// wipe models Caddy losing its routes (restarted from a bare config).
func (c *fakeCaddy) wipe() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.config = []byte("{}")
}

// liveHosts returns every route host in the live config, so tests can tell
// what Caddy is actually serving.
func (c *fakeCaddy) liveHosts(t *testing.T) []string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var cfg struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Routes []struct {
						Match []struct {
							Host []string `json:"host"`
						} `json:"match"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal(c.config, &cfg); err != nil {
		t.Fatalf("parse live config: %v", err)
	}
	var hosts []string
	for _, srv := range cfg.Apps.HTTP.Servers {
		for _, r := range srv.Routes {
			for _, m := range r.Match {
				hosts = append(hosts, m.Host...)
			}
		}
	}
	return hosts
}

func (c *fakeCaddy) loadCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loads
}

// fakeLAN is a LANResolver with a fixed answer, so LAN commands never look
// at the machine's real interfaces.
type fakeLAN struct {
	ip      netip.Addr
	present bool
}

func (f fakeLAN) Discover() (lanip.Result, error) { return lanip.Result{IP: f.ip}, nil }
func (f fakeLAN) Present(netip.Addr) (bool, error) {
	return f.present, nil
}

// testEnv is one fully faked caddy_shack install: temporary state and cert
// paths, a docker.Fake, a fake Caddy admin API, a fake ACME issuer and a
// fake LAN resolver, wired into the App every command gets.
type testEnv struct {
	paths  app.Paths
	dock   *docker.Fake
	caddy  *fakeCaddy
	issuer *acme.FakeIssuer
	app    *app.App
}

// fakeCertResult is what the fake issuer hands back: placeholder PEM that
// expires in 90 days, so a fresh certificate is not due for renewal.
func fakeCertResult() acme.Result {
	return acme.Result{
		CertPEM:  []byte("-----BEGIN CERTIFICATE-----\nCERT\n-----END CERTIFICATE-----\n"),
		KeyPEM:   []byte("-----BEGIN EC PRIVATE KEY-----\nKEY\n-----END EC PRIVATE KEY-----\n"),
		NotAfter: time.Now().Add(90 * 24 * time.Hour),
	}
}

// newTestEnv builds a testEnv with no Caddy container yet and points every
// command at it. It swaps the package-level newApp, so a test that calls it
// must not run in parallel. The swap is undone on cleanup.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	e := &testEnv{
		paths: app.Paths{
			StatePath:    filepath.Join(dir, "state.json"),
			LockPath:     filepath.Join(dir, "state.json.lock"),
			CertStoreDir: dir,
		},
		dock:  docker.NewFake(),
		caddy: &fakeCaddy{config: []byte("{}")},
	}
	srv := httptest.NewServer(e.caddy)
	t.Cleanup(srv.Close)

	e.app = app.NewWithPaths(e.paths, caddyapi.NewClient(srv.URL, time.Second), e.dock).
		WithLANResolver(fakeLAN{ip: netip.MustParseAddr("192.168.1.20"), present: true})
	e.setIssuer(acme.NewFakeIssuer(fakeCertResult(), nil))

	prev := newApp
	newApp = func() (*app.App, error) { return e.app, nil }
	t.Cleanup(func() { newApp = prev })
	return e
}

// setIssuer replaces the ACME issuer the App orders certificates from.
func (e *testEnv) setIssuer(iss *acme.FakeIssuer) {
	e.issuer = iss
	e.app.SetIssuer(iss)
}

// newInstalledEnv is newTestEnv after a successful 'caddy_shack install', so
// the container exists with the spec this engine wants and Caddy is up.
func newInstalledEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newTestEnv(t)
	e.mustRun(t, "", "install")
	return e
}

// loadState reads the registry as the commands left it.
func (e *testEnv) loadState(t *testing.T) *state.State {
	t.Helper()
	s, err := state.Load(e.paths.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return s
}

// result is the outcome of one CLI invocation.
type result struct {
	stdout, stderr string
	err            error
	code           int
}

// run executes the real root command with args, feeding stdin, and reports
// what a script would see: both streams and the process exit code.
func (e *testEnv) run(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetArgs(args)
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&out)
	root.SetErr(&errOut)
	err := execute(t.Context(), root)
	return result{stdout: out.String(), stderr: errOut.String(), err: err, code: app.ExitCode(err)}
}

// mustRun is run for setup steps that have to succeed.
func (e *testEnv) mustRun(t *testing.T, stdin string, args ...string) result {
	t.Helper()
	r := e.run(t, stdin, args...)
	if r.err != nil {
		t.Fatalf("%v: unexpected error: %v\nstdout:\n%s\nstderr:\n%s", args, r.err, r.stdout, r.stderr)
	}
	return r
}

// wantCode fails the test unless r exited with code, and when a sentinel is
// given, unless the error wraps it.
func wantCode(t *testing.T, r result, code int, sentinel error) {
	t.Helper()
	if r.code != code {
		t.Errorf("exit code = %d, want %d (err: %v)", r.code, code, r.err)
	}
	if sentinel != nil && !errors.Is(r.err, sentinel) {
		t.Errorf("err = %v, want it to wrap %v", r.err, sentinel)
	}
}

// writeCorruptState replaces state.json with something that does not parse.
func writeCorruptState(t *testing.T, e *testEnv) {
	t.Helper()
	if err := os.WriteFile(e.paths.StatePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// holdStateLock takes the state lock as another caddy_shack process would,
// returning the release. Commands that need the lock wait out the app's 5s
// timeout and then fail with ErrLockContention.
func holdStateLock(t *testing.T, e *testEnv) func() {
	t.Helper()
	l, err := state.AcquireLock(e.paths.LockPath, time.Second)
	if err != nil {
		t.Fatalf("take state lock: %v", err)
	}
	return func() { _ = l.Release() }
}
