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

// shrinkRelayWait bounds the relay readiness wait for one test.
func shrinkRelayWait(t *testing.T) {
	t.Helper()
	savedTimeout, savedInterval := relayReadyTimeout, relayReadyInterval
	relayReadyTimeout, relayReadyInterval = 50*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { relayReadyTimeout, relayReadyInterval = savedTimeout, savedInterval })
}

// relayProbeKey is the fake's Exec key for the relay readiness probe.
func relayProbeKey() string {
	return CaddyContainerName + ":" + strings.Join(relayProbeArgv(), " ")
}

// TestStartReportsRelayThatNeverAccepts: docker start returns before the
// relay's Caddy binds its socket, so until it does every host-mode route is
// a 502. Start waits for the socket and reports a relay that never accepts.
func TestStartReportsRelayThatNeverAccepts(t *testing.T) {
	shrinkRelayWait(t)
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	useNativeLinux(t, d)
	d.Containers[CaddyContainerName] = docker.StateStopped
	d.ExecResponses[relayProbeKey()] = docker.ExecResponse{Err: errors.New("curl: (7) Couldn't connect")}
	seedState(t, f.statePath, "myapp", "myapp.test", "app:3000")

	err := f.app.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "relay") || !strings.Contains(err.Error(), "Couldn't connect") {
		t.Fatalf("want the unready relay reported, got %v", err)
	}
}

func TestStartReturnsOnceRelayAccepts(t *testing.T) {
	shrinkRelayWait(t)
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	useNativeLinux(t, d)
	d.Containers[CaddyContainerName] = docker.StateStopped
	seedState(t, f.statePath, "myapp", "myapp.test", "app:3000")

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

// TestStartLeavesRunningRelayUnprobed: a relay that was already running and
// matches its spec is serving, so Start does not wait on it.
func TestStartLeavesRunningRelayUnprobed(t *testing.T) {
	shrinkRelayWait(t)
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	useNativeLinux(t, d)
	seedCurrentCaddy(d)
	d.ExecResponses[relayProbeKey()] = docker.ExecResponse{Err: errors.New("probed a running relay")}
	seedState(t, f.statePath, "myapp", "myapp.test", "app:3000")

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
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

func TestStartAndInstallDoNotDockerStartARestartingContainer(t *testing.T) {
	for name, run := range map[string]func(*App) error{
		"Start":   func(a *App) error { return a.Start(context.Background()) },
		"Install": func(a *App) error { return a.Install(context.Background()) },
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, http.StatusOK)
			d := f.app.docker.(*docker.Fake)
			// Install once so the container matches the expected spec (no drift),
			// then make it crash-loop.
			if err := f.app.Install(context.Background()); err != nil {
				t.Fatalf("setup Install: %v", err)
			}
			d.Containers[CaddyContainerName] = docker.StateRestarting

			if err := run(f.app); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			// Fake.Start flips the state to running, so an unchanged state
			// proves docker start was not called.
			st, _ := d.ContainerState(CaddyContainerName)
			if st != docker.StateRestarting {
				t.Errorf("state = %v, want restarting (docker start must not be called)", st)
			}
		})
	}
}

func TestStopStopsARestartingContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRestarting
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if st, _ := fake.ContainerState(CaddyContainerName); st != docker.StateStopped {
		t.Errorf("state = %v, want stopped (docker stop must be issued)", st)
	}
}
