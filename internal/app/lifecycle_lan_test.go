package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// recDocker records the mutating and inspecting docker calls in order.
type recDocker struct {
	docker.Client
	calls    []string
	onCreate func(name string)
	onPull   func(image string)
}

func (r *recDocker) rec(s string) { r.calls = append(r.calls, s) }
func (r *recDocker) ContainerState(n string) (docker.ContainerState, error) {
	r.rec("state:" + n)
	return r.Client.ContainerState(n)
}
func (r *recDocker) Start(n string) error { r.rec("start:" + n); return r.Client.Start(n) }
func (r *recDocker) Stop(n string) error  { r.rec("stop:" + n); return r.Client.Stop(n) }
func (r *recDocker) Remove(n string) error {
	r.rec("remove:" + n)
	return r.Client.Remove(n)
}
func (r *recDocker) CreateAndStart(spec docker.ContainerSpec) error {
	r.rec("create:" + spec.Name)
	if r.onCreate != nil {
		r.onCreate(spec.Name)
	}
	return r.Client.CreateAndStart(spec)
}
func (r *recDocker) PullImage(i string) error {
	r.rec("pull:" + i)
	if r.onPull != nil {
		r.onPull(i)
	}
	return r.Client.PullImage(i)
}
func (r *recDocker) EnsureNetwork(n string) error {
	r.rec("network:" + n)
	return r.Client.EnsureNetwork(n)
}

func saveLAN(t *testing.T, f *fixture, ip string, mode state.LANMode) {
	t.Helper()
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "host.docker.internal:3000", RegisteredAt: "2026-06-22T00:00:00Z"},
		},
		LAN: &state.LAN{Enabled: true, IP: ip, Mode: mode, Suffix: "lan"},
	}
	if err := state.Save(f.statePath, s); err != nil {
		t.Fatal(err)
	}
}

func notices(f *fixture) *strings.Builder {
	var b strings.Builder
	f.app.notices = &b
	return &b
}

func TestStartEnsuresLANAfterRoutes(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	rec := &recDocker{Client: f.app.docker}
	f.app.docker = rec
	loadsAtCreate := -1
	rec.onCreate = func(name string) {
		if name == LANContainerName {
			loadsAtCreate = f.loads()
		}
	}
	if err := f.app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loadsAtCreate < 1 {
		t.Errorf("forwarder created with %d Caddy loads done; routes must be loaded first", loadsAtCreate)
	}
	if _, ok := rec.Client.(*docker.Fake).Specs[LANContainerName]; !ok {
		t.Error("forwarder not created")
	}
}

func TestStartLANDownIsNotice(t *testing.T) {
	f := lanFixture(t, discoverStub{present: false})
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	buf := notices(f)
	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("LAN down must not fail start: %v", err)
	}
	want := "notice: LAN access is off: 192.168.1.12 is not on any interface. Reconnect to that network and run " +
		"'caddy_shack restart' (discovered mode rebinds automatically; pinned mode: 'caddy_shack lan enable --ip <new>').\n"
	if !strings.Contains(buf.String(), want) {
		t.Errorf("notice: %q", buf.String())
	}
	if _, ok := f.app.docker.(*docker.Fake).Specs[LANContainerName]; ok {
		t.Error("forwarder should not be created")
	}
}

func TestStopStopsLAN(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	d := f.app.docker.(*docker.Fake)
	if err := f.app.ensureLAN(lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	if err := f.app.Stop(); err != nil {
		t.Fatal(err)
	}
	if st := d.Containers[LANContainerName]; st != docker.StateStopped {
		t.Errorf("forwarder state: %v", st)
	}
}

func TestRestartRediscoversWhenDiscovered(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	d := f.app.docker.(*docker.Fake)
	if err := f.app.ensureLAN(lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	buf := notices(f)
	if err := f.app.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	if s.LAN.IP != "192.168.1.50" {
		t.Errorf("state IP: %s", s.LAN.IP)
	}
	if got := d.Infos[LANContainerName].Ports[0].HostAddr; got != "192.168.1.50" {
		t.Errorf("forwarder bound to %s", got)
	}
	if !strings.Contains(buf.String(), "192.168.1.12 -> 192.168.1.50") {
		t.Errorf("notice: %q", buf.String())
	}
}

func TestRestartKeepsPinnedIP(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	if err := f.app.Restart(context.Background()); err != nil {
		t.Fatal(err)
	}
	s, _ := state.Load(f.statePath)
	if s.LAN.IP != "192.168.1.12" {
		t.Errorf("pinned IP changed to %s", s.LAN.IP)
	}
}

func TestRestartDiscoveryFailureIsNotice(t *testing.T) {
	f := lanFixture(t, discoverStub{derr: errors.New("no interface"), present: true})
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	buf := notices(f)
	if err := f.app.Restart(context.Background()); err != nil {
		t.Fatalf("discovery failure must not fail restart: %v", err)
	}
	if !strings.Contains(buf.String(), "no interface") {
		t.Errorf("notice: %q", buf.String())
	}
	s, _ := state.Load(f.statePath)
	if s.LAN.IP != "192.168.1.12" {
		t.Errorf("IP changed to %s", s.LAN.IP)
	}
}

func upgradeLANFixture(t *testing.T) (*fixture, *docker.Fake) {
	t.Helper()
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:caddy"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:caddy"
	d.ImageIDs[LANImage] = "sha256:new"
	if err := f.app.ensureLAN(lanState("192.168.1.12")); err != nil {
		t.Fatal(err)
	}
	d.Removed = nil
	return f, d
}

func TestUpgradeReplacesStaleForwarder(t *testing.T) {
	f, d := upgradeLANFixture(t)
	d.ContainerImageIDs[LANContainerName] = "sha256:old"
	rep, err := f.app.Upgrade(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.AlreadyCurrent {
		t.Error("expected the AlreadyCurrent path")
	}
	if !d.PulledImg[LANImage] {
		t.Error("forwarder image not pulled")
	}
	if !reflect.DeepEqual(d.Removed, []string{LANContainerName}) {
		t.Errorf("removed: %v", d.Removed)
	}
	if d.Containers[LANContainerName] != docker.StateRunning {
		t.Error("forwarder not recreated")
	}
}

func TestUpgradeLeavesCurrentForwarder(t *testing.T) {
	f, d := upgradeLANFixture(t)
	d.ContainerImageIDs[LANContainerName] = "sha256:new"
	if _, err := f.app.Upgrade(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(d.Removed) != 0 {
		t.Errorf("current forwarder replaced: %v", d.Removed)
	}
}

func TestSyncEnsuresLAN(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	if _, err := f.app.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.app.docker.(*docker.Fake).Specs[LANContainerName]; !ok {
		t.Error("forwarder not created")
	}
}

func TestInstallEnsuresLAN(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	if err := f.app.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.app.docker.(*docker.Fake).Specs[LANContainerName]; !ok {
		t.Error("forwarder not created")
	}
}

// TestLifecycleNoLANUnchanged pins the docker call sequence with no LAN
// settings: the forwarder must not be asked about at all.
func TestLifecycleNoLANUnchanged(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")
	f.app.WithLANResolver(stubLAN{present: true})
	rec := &recDocker{Client: f.app.docker}
	f.app.docker = rec
	ctx := context.Background()

	if err := f.app.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.calls, []string{"state:caddy_shack"}; !reflect.DeepEqual(got, want) {
		t.Errorf("start: %v", got)
	}
	rec.calls = nil
	if err := f.app.Stop(); err != nil {
		t.Fatal(err)
	}
	if got, want := rec.calls, []string{"state:caddy_shack", "stop:caddy_shack", "state:caddy_shack_relay"}; !reflect.DeepEqual(got, want) {
		t.Errorf("stop: %v", got)
	}
	rec.calls = nil
	if err := f.app.Restart(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"state:caddy_shack", "state:caddy_shack_relay",
		"state:caddy_shack", "start:caddy_shack", "network:caddy_shack",
	}
	if !reflect.DeepEqual(rec.calls, want) {
		t.Errorf("restart: %v", rec.calls)
	}
}

// shortLockTimeout makes a held state lock fail fast instead of after the
// production timeout.
func shortLockTimeout(t *testing.T) {
	t.Helper()
	old := stateLockTimeout
	stateLockTimeout = 50 * time.Millisecond
	t.Cleanup(func() { stateLockTimeout = old })
}

// Rediscovery runs between Stop and Start, so its failure must never leave
// Caddy stopped.
func TestRestartStartsCaddyWhenStateLockHeld(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	shortLockTimeout(t)
	lock, err := state.AcquireLock(f.statePath+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	buf := notices(f)
	_ = f.app.Restart(context.Background())
	d := f.app.docker.(*docker.Fake)
	if d.Containers[CaddyContainerName] != docker.StateRunning {
		t.Errorf("caddy left %v after restart", d.Containers[CaddyContainerName])
	}
	if !strings.Contains(buf.String(), "could not rediscover the LAN IP") {
		t.Errorf("notice: %q", buf.String())
	}
}

func TestRestartStartsCaddyWhenStateCorrupt(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.app.Restart(context.Background())
	if !errors.Is(err, ErrStateCorrupt) {
		t.Errorf("want Start's ErrStateCorrupt, got %v", err)
	}
	d := f.app.docker.(*docker.Fake)
	if d.Containers[CaddyContainerName] != docker.StateRunning {
		t.Errorf("caddy left %v after restart", d.Containers[CaddyContainerName])
	}
}

// Without discovered LAN settings rediscovery is skipped without taking the
// state lock, so a held lock does not delay the restart.
func TestRestartSkipsRediscoveryWithoutDiscoveredLAN(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	shortLockTimeout(t)
	lock, err := state.AcquireLock(f.statePath+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	buf := notices(f)
	_ = f.app.Restart(context.Background())
	if strings.Contains(buf.String(), "rediscover") {
		t.Errorf("pinned mode tried to rediscover: %q", buf.String())
	}
	if d := f.app.docker.(*docker.Fake); d.Containers[CaddyContainerName] != docker.StateRunning {
		t.Errorf("caddy left %v after restart", d.Containers[CaddyContainerName])
	}
}

func TestStartForwarderBindFailureIsNotice(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	d := f.app.docker.(*docker.Fake)
	d.CreateErrs = map[string]error{LANContainerName: errors.New("Bind for 192.168.1.12:443 failed: port is already allocated")}
	buf := notices(f)
	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("forwarder bind failure must not fail start: %v", err)
	}
	for _, want := range []string{"192.168.1.12:443 is already in use", "caddy_shack doctor"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("notice missing %q: %q", want, buf.String())
		}
	}
}

func TestStartForwarderStartFailureIsNotice(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	d := f.app.docker.(*docker.Fake)
	d.CreateErrs = map[string]error{LANContainerName: errors.New("driver failed programming external connectivity")}
	buf := notices(f)
	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("forwarder start failure must not fail start: %v", err)
	}
	for _, want := range []string{"192.168.1.12:443", "192.168.1.12:80", "driver failed", "caddy_shack doctor"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("notice missing %q: %q", want, buf.String())
		}
	}
}
