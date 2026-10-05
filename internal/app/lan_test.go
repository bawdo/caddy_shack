package app

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"testing"

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
