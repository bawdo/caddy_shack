package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// seedState writes a single-backend state.json at path, for tests that need
// Start/Restart to have something to restore into Caddy.
func seedState(t *testing.T, path, name, domain, upstream string) {
	t.Helper()
	s := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			name: {Domain: domain, Upstream: upstream, RegisteredAt: "2026-06-22T00:00:00Z"},
		},
	}
	if err := state.Save(path, s); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

func TestStartReturnsErrNotInstalledWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	err := a.Start(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestStartStartsStoppedContainerAndRestoresBackends(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateStopped
	seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st, _ := d.ContainerState(CaddyContainerName)
	if st != docker.StateRunning {
		t.Errorf("state: %v", st)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("backends not restored to Caddy: %s", f.lastBody)
	}
}

func TestStartOnRunningInSyncCaddyPushesNothing(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")
	f.syncLive(t)

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if n := f.loads(); n != 0 {
		t.Errorf("in-sync Caddy should not be re-pushed; got %d loads", n)
	}
}

// TestStartOnRunningCaddyRestoresLostRoutes is the incident: the container
// was restarted outside caddy_shack (host reboot) and came back with only
// its admin config. Start finds it running and must still restore routes.
func TestStartOnRunningCaddyRestoresLostRoutes(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("backends not restored to a running Caddy: %s", f.lastBody)
	}
}

// TestStartRestoresRoutesEvenWhenRelayFails: routes go in before the relay
// is ensured, so a relay failure leaves only host-mode backends down
// instead of a Caddy with no routes at all.
func TestStartRestoresRoutesEvenWhenRelayFails(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	useNativeLinux(t, d)
	d.Containers[CaddyContainerName] = docker.StateStopped
	d.CreateErrs = map[string]error{RelayContainerName: errors.New("relay boom")}
	seedState(t, f.statePath, "myapp", "myapp.test", "app:3000")

	err := f.app.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "relay boom") {
		t.Fatalf("want the relay failure reported, got %v", err)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("routes not restored before the relay failed: %s", f.lastBody)
	}
}

func TestStartOnStoppedContainerFailsWhenAdminNeverComesUp(t *testing.T) {
	savedTimeout, savedInterval := adminReadyTimeout, adminReadyInterval
	adminReadyTimeout, adminReadyInterval = 50*time.Millisecond, 10*time.Millisecond
	defer func() { adminReadyTimeout, adminReadyInterval = savedTimeout, savedInterval }()

	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateStopped
	f.caddySrv.Close() // admin API never comes up after the start

	err := f.app.Start(context.Background())
	if err == nil {
		t.Fatal("expected an error when the admin API never becomes reachable")
	}
}

func TestStopIsNoOpWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Errorf("expected nil err on absent stop, got %v", err)
	}
}

func TestStopStopsRunningContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	st, _ := fake.ContainerState(CaddyContainerName)
	if st != docker.StateStopped {
		t.Errorf("state: %v", st)
	}
}

func TestRestartStopsThenStartsAndRestoresBackends(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateRunning
	seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")

	if err := f.app.Restart(context.Background()); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	st, _ := d.ContainerState(CaddyContainerName)
	if st != docker.StateRunning {
		t.Errorf("state: %v", st)
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("backends not restored to Caddy after restart: %s", f.lastBody)
	}
}
