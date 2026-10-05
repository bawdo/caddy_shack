package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

type stubLAN struct {
	present bool
	err     error
}

func (s stubLAN) Discover() (lanip.Result, error) { return lanip.Result{}, s.err }
func (s stubLAN) Present(netip.Addr) (bool, error) {
	return s.present, s.err
}

func lanState(ip string) *state.State {
	return &state.State{LAN: &state.LAN{Enabled: true, IP: ip, Mode: state.LANModePinned, Suffix: "lan"}}
}

func newLANApp(present bool) (*App, *docker.Fake) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake).WithLANResolver(stubLAN{present: present})
	return a, fake
}

func TestEnsureLANDisabledRemovesForwarder(t *testing.T) {
	a, fake := newLANApp(true)
	fake.Containers[LANContainerName] = docker.StateRunning
	if err := a.ensureLAN(&state.State{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.Containers[LANContainerName]; ok {
		t.Error("forwarder should be removed")
	}
}

func TestEnsureLANCreatesWhenAbsent(t *testing.T) {
	a, fake := newLANApp(true)
	if err := a.ensureLAN(lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	spec, ok := fake.Specs[LANContainerName]
	if !ok {
		t.Fatal("forwarder not created")
	}
	if spec.Image != LANImage || spec.NetworkMode != CaddyNetworkName {
		t.Errorf("image/network: %q %q", spec.Image, spec.NetworkMode)
	}
	want := []docker.PortMapping{
		{HostAddr: "192.168.1.12", HostPort: 443, ContainerPort: 8443},
		{HostAddr: "192.168.1.12", HostPort: 80, ContainerPort: 8080},
	}
	got := spec.PortMappings
	docker.SortPortMappings(got)
	docker.SortPortMappings(want)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("ports: got %v want %v", got, want)
	}
	if !fake.Networks[CaddyNetworkName] {
		t.Error("network not ensured")
	}
}

func TestEnsureLANStartsStopped(t *testing.T) {
	a, fake := newLANApp(true)
	s := lanState("192.168.1.12")
	if err := a.ensureLAN(s); err != nil {
		t.Fatal(err)
	}
	fake.Containers[LANContainerName] = docker.StateStopped
	fake.Removed = nil
	if err := a.ensureLAN(s); err != nil {
		t.Fatal(err)
	}
	if fake.Containers[LANContainerName] != docker.StateRunning {
		t.Error("should be started")
	}
	if len(fake.Removed) != 0 {
		t.Errorf("should not be replaced: %v", fake.Removed)
	}
}

func TestEnsureLANReplacesOnIPDrift(t *testing.T) {
	a, fake := newLANApp(true)
	if err := a.ensureLAN(lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	if err := a.ensureLAN(lanState("192.168.1.99")); err != nil {
		t.Fatal(err)
	}
	if len(fake.Removed) != 1 {
		t.Errorf("removed: %v", fake.Removed)
	}
	if got := fake.Infos[LANContainerName].Ports[0].HostAddr; got != "192.168.1.99" {
		t.Errorf("host addr: %s", got)
	}
}

func TestEnsureLANIPMissingTouchesNothing(t *testing.T) {
	a, fake := newLANApp(false)
	err := a.ensureLAN(lanState("192.168.1.12"))
	if !errors.Is(err, ErrLANDown) {
		t.Fatalf("want ErrLANDown, got %v", err)
	}
	if !strings.Contains(err.Error(), "192.168.1.12") {
		t.Errorf("error should name the IP: %v", err)
	}
	if len(fake.Containers) != 0 || len(fake.Networks) != 0 || len(fake.Removed) != 0 || len(fake.Specs) != 0 {
		t.Error("docker was touched")
	}
}

func TestEnsureLANRewrapsAddressInUse(t *testing.T) {
	a, fake := newLANApp(true)
	fake.CreateErrs = map[string]error{LANContainerName: errors.New("Bind for 192.168.1.12:443 failed: address already in use")}
	err := a.ensureLAN(lanState("192.168.1.12"))
	if err == nil || !strings.Contains(err.Error(), "192.168.1.12:443 is already in use by another process") {
		t.Fatalf("got %v", err)
	}
	if _, ok := fake.Containers[LANContainerName]; ok {
		t.Error("failed forwarder should be removed")
	}
}

func TestHaproxyConfigPointsAtCaddy(t *testing.T) {
	cfg := haproxyConfig("caddy_shack")
	for _, want := range []string{"caddy_shack:8443 send-proxy-v2", "caddy_shack:8080 send-proxy-v2", "init-addr none"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q", want)
		}
	}
}

func TestNoteLANDownSwallowsOnlyLANDown(t *testing.T) {
	a, _ := newLANApp(true)
	var buf strings.Builder
	a.notices = &buf
	if err := a.noteLANDown(fmt.Errorf("x: %w", ErrLANDown)); err != nil {
		t.Errorf("should swallow: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("expected a notice")
	}
	other := errors.New("boom")
	if err := a.noteLANDown(other); !errors.Is(err, other) {
		t.Errorf("should pass through: %v", err)
	}
}

func TestStopLANStopsRunningAndIsSafeWhenAbsent(t *testing.T) {
	a, fake := newLANApp(true)
	if err := a.stopLAN(); err != nil {
		t.Fatal(err)
	}
	fake.Containers[LANContainerName] = docker.StateRunning
	if err := a.stopLAN(); err != nil {
		t.Fatal(err)
	}
	if fake.Containers[LANContainerName] != docker.StateStopped {
		t.Error("should be stopped")
	}
}

// discoverStub is a LANResolver whose Discover and Present are scripted.
type discoverStub struct {
	res     lanip.Result
	derr    error
	present bool
}

func (d discoverStub) Discover() (lanip.Result, error) { return d.res, d.derr }
func (d discoverStub) Present(netip.Addr) (bool, error) {
	return d.present, nil
}

func lanFixture(t *testing.T, r LANResolver) *fixture {
	t.Helper()
	f := newFixture(t, http.StatusOK)
	f.app.WithLANResolver(r)
	return f
}

func discovered(ip string) discoverStub {
	return discoverStub{res: lanip.Result{IP: netip.MustParseAddr(ip), Note: "picked en0"}, present: true}
}

func TestLANEnableDiscovered(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	res, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "arrix.home.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IP != "192.168.1.12" || res.Mode != state.LANModeDiscovered || res.Note != "picked en0" {
		t.Errorf("result: %+v", res)
	}
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if !s.LANEnabled() || s.LAN.IP != "192.168.1.12" || s.LAN.Mode != state.LANModeDiscovered || s.LAN.Suffix != "arrix.home.lan" {
		t.Errorf("state: %+v", s.LAN)
	}
	if _, ok := f.app.docker.(*docker.Fake).Specs[LANContainerName]; !ok {
		t.Error("forwarder not created")
	}
}

func TestLANEnablePinnedAbsentIP(t *testing.T) {
	f := lanFixture(t, discoverStub{present: false})
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{IP: "192.168.1.50", Suffix: "lan"})
	if !errors.Is(err, ErrInvalidArgs) || !strings.Contains(err.Error(), "192.168.1.50") {
		t.Fatalf("got %v", err)
	}
	if _, statErr := os.Stat(f.statePath); statErr == nil {
		t.Error("state was written")
	}
}

func TestLANEnablePinnedMode(t *testing.T) {
	f := lanFixture(t, discoverStub{present: true})
	res, err := f.app.LANEnable(context.Background(), LANEnableInput{IP: "192.168.1.50", Suffix: "lan"})
	if err != nil || res.Mode != state.LANModePinned {
		t.Fatalf("res %+v err %v", res, err)
	}
	for _, bad := range []string{"127.0.0.1", "::1", "nonsense"} {
		_, err := f.app.LANEnable(context.Background(), LANEnableInput{IP: bad, Suffix: "lan"})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%s: got %v", bad, err)
		}
	}
}

func TestLANEnableDiscoverErrorIsInvalidArgs(t *testing.T) {
	f := lanFixture(t, discoverStub{derr: lanip.ErrNoCandidate})
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "lan"})
	if !errors.Is(err, ErrInvalidArgs) || !errors.Is(err, lanip.ErrNoCandidate) {
		t.Fatalf("got %v", err)
	}
}

func TestLANEnableRequiresSuffixFirstTime(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("got %v", err)
	}
}

func TestLANEnableRejectsTestSuffix(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	for _, suf := range []string{"arrix.test", "test", "Bad_Name", "-x"} {
		_, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: suf})
		if !errors.Is(err, ErrInvalidArgs) {
			t.Errorf("%q: got %v", suf, err)
		}
	}
}

func TestLANEnableRequiresRunningCaddy(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	f.app.docker.(*docker.Fake).Containers[CaddyContainerName] = docker.StateStopped
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "lan"})
	if !errors.Is(err, ErrCaddyNotRunning) {
		t.Fatalf("got %v", err)
	}
}

func TestLANEnableKeepsExistingLANDomains(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	seed := &state.State{
		Backends: map[string]state.Backend{"gamma": {Domain: "gamma.test", Upstream: "host.docker.internal:3000", LANDomain: "gamma.arrix.home.lan"}},
		LAN:      &state.LAN{Enabled: false, IP: "192.168.1.12", Mode: state.LANModeDiscovered, Suffix: "arrix.home.lan"},
	}
	if err := state.Save(f.statePath, seed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "other.lan"}); err != nil {
		t.Fatal(err)
	}
	s, _ := state.Load(f.statePath)
	if got := s.Backends["gamma"].LANDomain; got != "gamma.arrix.home.lan" {
		t.Errorf("LANDomain changed to %q", got)
	}
	if s.LAN.Suffix != "other.lan" {
		t.Errorf("suffix %q", s.LAN.Suffix)
	}
}

func TestLANEnableKeepsStoredSuffix(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	_ = state.Save(f.statePath, &state.State{LAN: &state.LAN{Suffix: "kept.lan"}})
	res, err := f.app.LANEnable(context.Background(), LANEnableInput{})
	if err != nil || res.Suffix != "kept.lan" {
		t.Fatalf("res %+v err %v", res, err)
	}
}

func TestLANEnableConfirmDeclined(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	var asked string
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{
		Suffix:  "lan",
		Confirm: func(l string) bool { asked = l; return false },
	})
	if err == nil || err.Error() != "cancelled" {
		t.Fatalf("got %v", err)
	}
	if asked != "Bind LAN access to 192.168.1.12 (discovered)? " {
		t.Errorf("prompt %q", asked)
	}
	if _, statErr := os.Stat(f.statePath); statErr == nil {
		t.Error("state was written")
	}
	if f.loads() != 0 {
		t.Error("caddy was touched")
	}
}

// The IP vanishes between discovery and binding: state is already saved and
// pushed, and the error is ErrLANDown.
type vanishingResolver struct{ calls int }

func (v *vanishingResolver) Discover() (lanip.Result, error) {
	return lanip.Result{IP: netip.MustParseAddr("192.168.1.12")}, nil
}
func (v *vanishingResolver) Present(netip.Addr) (bool, error) {
	v.calls++
	return false, nil
}

func TestLANEnableIPMissingAfterSave(t *testing.T) {
	f := lanFixture(t, &vanishingResolver{})
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "lan"})
	if !errors.Is(err, ErrLANDown) {
		t.Fatalf("got %v", err)
	}
	s, _ := state.Load(f.statePath)
	if !s.LANEnabled() || f.loads() != 1 {
		t.Errorf("state saved=%v loads=%d", s.LANEnabled(), f.loads())
	}
}

func TestLANDisableRemovesForwarderBeforeConfig(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.Containers[LANContainerName] = docker.StateRunning
	var forwarderAtLoad []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			_, present := fake.Containers[LANContainerName]
			forwarderAtLoad = append(forwarderAtLoad, present)
		}
	}))
	defer srv.Close()
	dir := t.TempDir()
	paths := Paths{StatePath: filepath.Join(dir, "state.json"), LockPath: filepath.Join(dir, "state.lock")}
	a := NewWithPaths(paths, caddyapi.NewClient(srv.URL, time.Second), fake)
	if err := state.Save(paths.StatePath, lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	if err := a.LANDisable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(forwarderAtLoad) != 1 || forwarderAtLoad[0] {
		t.Errorf("forwarder present at /load: %v", forwarderAtLoad)
	}
}

func TestLANDisableKeepsSettings(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	seed := lanState("192.168.1.12")
	seed.Backends = map[string]state.Backend{"gamma": {Domain: "gamma.test", Upstream: "host.docker.internal:3000", LANDomain: "gamma.lan"}}
	_ = state.Save(f.statePath, seed)
	if err := f.app.LANDisable(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := state.Load(f.statePath)
	if s.LAN == nil || s.LAN.Enabled || s.LAN.IP != "192.168.1.12" || s.LAN.Suffix != "lan" || s.LAN.Mode != state.LANModePinned {
		t.Errorf("LAN: %+v", s.LAN)
	}
	if s.Backends["gamma"].LANDomain != "gamma.lan" {
		t.Error("LANDomain touched")
	}
	if strings.Contains(string(f.lastBody), "srv-lan") {
		t.Error("config still has LAN servers")
	}
}
