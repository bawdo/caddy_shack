package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
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
