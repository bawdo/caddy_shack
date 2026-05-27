package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func TestCheckDockerDaemonPass(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("docker-daemon has no fix")
	}
}

func TestCheckDockerDaemonFail(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Name != "docker-daemon" {
		t.Errorf("name: %q", r.Name)
	}
}

// listenLoopback grabs a free loopback port the test holds open until cleanup.
// Returns the port number for the caller to feed back into the check.
func listenLoopback(t *testing.T) (int, *net.TCPListener) {
	t.Helper()
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port, l
}

func TestCheckPortAvailabilityFailWhenPortHeld(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateAbsent
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityFail {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	want := fmt.Sprintf("%d", heldPort)
	found := false
	for _, d := range r.Detail {
		if contains(d, want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected detail to mention port %d; got %v", heldPort, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenAllFreeInjected(t *testing.T) {
	// Use injected freePort set so the test does not race against the OS.
	p, l := listenLoopback(t)
	_ = l.Close() // free it for the probe
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	r := checkPortAvailabilityOn(context.Background(), a, []int{p})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenContainerRunningHoldsPorts(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && (len(haystack) >= len(needle)) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func newDoctorApp(t *testing.T, fake *docker.Fake, caddyURL string) *App {
	t.Helper()
	dir := t.TempDir()
	return NewWithPaths(Paths{
		StatePath: dir + "/state.json",
		LockPath:  dir + "/state.json.lock",
	}, caddyapi.NewClient(caddyURL, time.Second), fake)
}

func TestCheckContainerHealthPassWhenRunningAndReachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := newDoctorApp(t, fake, srv.URL)

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("no fix expected when healthy")
	}
}

func TestCheckContainerHealthFailWhenAbsentOffersInstallFix(t *testing.T) {
	fake := docker.NewFake()
	// container absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected install fix")
	}
	if r.Fix.RequiresSudo {
		t.Errorf("install fix must not require sudo")
	}
	// Invoking the fix should call Install via the fake docker.
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("container state after install fix: %v", state)
	}
}

func TestCheckContainerHealthFailWhenStoppedOffersStartFix(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected start fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if state := fake.Containers[CaddyContainerName]; state != docker.StateRunning {
		t.Errorf("state after start fix: %v", state)
	}
}

func TestCheckContainerHealthSkipWhenDaemonDown(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkContainerHealth(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

// scriptedShell returns canned output keyed by "<name> <args joined by space>".
func scriptedShell(t *testing.T, table map[string]struct {
	out []byte
	err error
}) shellExecutor {
	t.Helper()
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		key := name
		if len(args) > 0 {
			key = name + " " + strings.Join(args, " ")
		}
		v, ok := table[key]
		if !ok {
			t.Errorf("unexpected shell call: %q", key)
			return nil, errors.New("unexpected")
		}
		return v.out, v.err
	}
}

func TestCheckDNSPassWhenDscacheReturnsLoopback(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	// The check should call dscacheutil first; if it returns 127.0.0.1 it
	// passes without invoking any sub-probes.
	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dscacheutil" {
			return []byte("name: anything.test\nip_address: 127.0.0.1\n"), nil
		}
		// Sub-probes only matter on failure; if invoked, return harmless
		// "found" responses.
		return []byte("ok"), nil
	}

	r := checkDNS(context.Background(), a, sh)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckDNSFailWhenDscacheReturnsNothing(t *testing.T) {
	a := newDoctorApp(t, docker.NewFake(), "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dscacheutil":
			return []byte(""), nil
		case "brew":
			if len(args) > 0 && args[0] == "list" {
				return nil, errors.New("not installed")
			}
			return []byte("/opt/homebrew"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkDNS(context.Background(), a, sh)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("DNS fail must offer fix")
	}
	if !r.Fix.RequiresSudo {
		t.Errorf("DNS fix requires sudo")
	}
}

// fakeRootCert returns a stable 200-byte payload and its hex SHA-256.
func fakeRootCert() ([]byte, string) {
	payload := bytes.Repeat([]byte{0xAB}, 200)
	sum := sha256.Sum256(payload)
	return payload, strings.ToUpper(hex.EncodeToString(sum[:]))
}

func TestCheckTrustStorePassWhenSHAFound(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, sha := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			// Emit the keychain dump containing the SHA we'd expect to find.
			return []byte("SHA-256 hash: " + sha + "\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStore(context.Background(), a, sh)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckTrustStoreFailWhenSHAMissing(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	cert, _ := fakeRootCert()
	fake.ExecResponses["caddy_shack:cat /data/caddy/pki/authorities/local/root.crt"] = docker.ExecResponse{
		Stdout: cert,
	}
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	sh := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "security" {
			return []byte("SHA-256 hash: DEADBEEF\n"), nil
		}
		return nil, errors.New("unexpected")
	}

	r := checkTrustStore(context.Background(), a, sh)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Fix == nil || !r.Fix.RequiresSudo {
		t.Errorf("fix: %+v", r.Fix)
	}
}

// noopShell is a do-nothing shellExecutor for tests that exercise paths
// where the shell executor is not consulted (e.g. the SKIP path before any
// shell-out happens).
var noopShell shellExecutor = func(context.Context, string, ...string) ([]byte, error) {
	return nil, nil
}

func TestCheckTrustStoreSkipWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")
	r := checkTrustStore(context.Background(), a, noopShell)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckTrustStoreWarnWhenCertNotProvisioned(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// No ExecResponses entry => Fake.Exec returns "no scripted response" error.
	a := newDoctorApp(t, fake, "http://127.0.0.1:1")

	r := checkTrustStore(context.Background(), a, noopShell)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckStateIntegrityPassWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckStateIntegrityFailWhenCorrupt(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	if err := os.WriteFile(statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckStateIntegrityWarnWhenLockHeld(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "state.json.lock")
	l, err := state.AcquireLock(lockPath, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Release() }()

	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  lockPath,
	}, nil, docker.NewFake())

	r := checkStateIntegrity(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckConfigDriftPassWhenRoutesMatch(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	desired := caddyapi.Build(st)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write(desired) // live config exactly matches generated
	}))
	defer srv.Close()

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckConfigDriftWarnWhenRoutesDiffer(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "127.0.0.1:1000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}

	// Live config: same shape, different upstream port - drift.
	live := map[string]any{
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"srv0": map[string]any{
						"routes": []any{
							map[string]any{
								"match": []any{map[string]any{"host": []any{"a.test"}}},
								"handle": []any{map[string]any{
									"handler":   "reverse_proxy",
									"upstreams": []any{map[string]any{"dial": "127.0.0.1:9999"}},
								}},
							},
						},
					},
				},
			},
		},
	}
	liveBytes, _ := json.Marshal(live)

	var loaded []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(200)
			_, _ = w.Write(liveBytes)
			return
		}
		// POST /load - record the body for fix-test below.
		loaded, _ = io.ReadAll(r.Body)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_ = loaded // for golangci - real assertion below

	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(srv.URL, time.Second), fake)

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix == nil {
		t.Fatal("expected re-load fix")
	}
	if err := r.Fix.Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Loaded body must have a route pointing at 127.0.0.1:1000 (from state),
	// not 127.0.0.1:9999 (the drifted live value).
	if !strings.Contains(string(loaded), "127.0.0.1:1000") {
		t.Errorf("re-loaded config does not contain state's upstream: %s", loaded)
	}
}

func TestCheckConfigDriftSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkConfigDrift(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
}

func TestCheckBackendsPassWhenAllReachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckBackendsFailWhenAnyUnreachable(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
		"b": {Domain: "b.test", Upstream: "host.docker.internal:4000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{}
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 4000"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	// Fallback to wget when nc fails - also fails.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:4000/"] = docker.ExecResponse{
		Err: errors.New("connection refused"),
	}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	found := false
	for _, d := range r.Detail {
		if strings.Contains(d, "b") || strings.Contains(d, ":4000") {
			found = true
		}
	}
	if !found {
		t.Errorf("detail must identify failing backend: %v", r.Detail)
	}
}

func TestCheckBackendsPassNoneRegistered(t *testing.T) {
	dir := t.TempDir()
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if !strings.Contains(r.Summary, "no backends") {
		t.Errorf("summary: %q", r.Summary)
	}
}

func TestCheckBackendsSkipWhenContainerNotRunning(t *testing.T) {
	dir := t.TempDir()
	a := NewWithPaths(Paths{
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "state.json.lock"),
	}, nil, docker.NewFake())

	r := checkBackends(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Errorf("status: %v", r.Status)
	}
}

func TestCheckBackendsUsesWgetFallbackWhenNcMissing(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	st := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"a": {Domain: "a.test", Upstream: "host.docker.internal:3000"},
	}}
	if err := state.Save(statePath, st); err != nil {
		t.Fatal(err)
	}
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	// nc missing.
	fake.ExecResponses["caddy_shack:nc -z -w 3 host.docker.internal 3000"] = docker.ExecResponse{
		Err: errors.New("nc: not found"),
	}
	// wget succeeds.
	fake.ExecResponses["caddy_shack:wget -q --spider --tries 1 --timeout 3 http://host.docker.internal:3000/"] = docker.ExecResponse{}
	a := NewWithPaths(Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, fake)

	r := checkBackends(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}
