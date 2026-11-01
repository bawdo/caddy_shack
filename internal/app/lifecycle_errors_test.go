package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/certstore"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// errInjected is the docker failure the tables below inject.
var errInjected = errors.New("injected docker failure")

// withUnreadableCert gives f a cert store holding one certificate whose key
// cannot be read, recorded in state, so the next buildConfig fails.
func withUnreadableCert(t *testing.T, f *fixture) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode 000 file")
	}
	f.app.certs = certstore.New(t.TempDir())
	const domain = "alpha.arrix.dev"
	if err := f.app.certs.Save(domain, []byte("CERT"), []byte("KEY")); err != nil {
		t.Fatal(err)
	}
	key := f.app.certs.KeyPath(domain)
	if err := os.Chmod(key, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(key, 0o600) })
	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	s.Certs[domain] = state.Cert{Domain: domain, Subjects: []string{domain, "*." + domain}, NotAfter: "2099-01-01T00:00:00Z"}
	saveState(t, f.statePath, s)
}

// holdStateLock takes the state lock for the rest of the test, with a short
// timeout so the code under test gives up fast.
func holdStateLock(t *testing.T, f *fixture) {
	t.Helper()
	shortLockTimeout(t)
	l, err := state.AcquireLock(f.statePath+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Release() })
}

// assertLockFree fails the test when the state lock is still held, i.e.
// the failure path leaked it.
func assertLockFree(t *testing.T, f *fixture) {
	t.Helper()
	if held, err := state.LockHeld(f.statePath + ".lock"); err != nil || held {
		t.Errorf("state lock held=%v (%v) after the failure", held, err)
	}
}

// TestStartFailuresRestoreNothing: Start pushes the registry only once the
// container is up and on the shared network, so an earlier failure leaves
// Caddy alone and is never mistaken for "not installed".
func TestStartFailuresRestoreNothing(t *testing.T) {
	cases := map[string]struct {
		st      docker.ContainerState
		inject  func(d *docker.Fake)
		wantErr string
	}{
		"inspect": {docker.StateRunning, func(d *docker.Fake) {
			d.ContainerStateErrs = map[string]error{CaddyContainerName: errInjected}
		}, "inspect container"},
		"docker start":   {docker.StateStopped, func(d *docker.Fake) { d.StartErr = errInjected }, errInjected.Error()},
		"shared network": {docker.StateStopped, func(d *docker.Fake) { d.ConnectNetworkErr = errInjected }, "attach shared network"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, http.StatusOK)
			seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")
			d := f.app.docker.(*docker.Fake)
			d.Containers[CaddyContainerName] = tc.st
			tc.inject(d)
			err := f.app.Start(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if ExitCode(err) != 1 {
				t.Errorf("exit = %d, want 1 (err %v)", ExitCode(err), err)
			}
			if f.loads() != 0 {
				t.Errorf("registry pushed %d times after a failed start", f.loads())
			}
		})
	}
}

// TestStopFailuresStopAtTheFirstError: Stop goes Caddy, relay, forwarder,
// and a failure leaves the later ones as they were rather than half
// stopping and reporting success.
func TestStopFailuresStopAtTheFirstError(t *testing.T) {
	cases := map[string]struct {
		inject      func(d *docker.Fake)
		wantErr     string
		caddyLeft   docker.ContainerState
		forwardLeft docker.ContainerState
	}{
		"caddy inspect": {
			func(d *docker.Fake) { d.ContainerStateErrs = map[string]error{CaddyContainerName: errInjected} },
			"inspect container", docker.StateRunning, docker.StateRunning,
		},
		"caddy stop": {
			func(d *docker.Fake) { d.StopErr = errInjected },
			errInjected.Error(), docker.StateRunning, docker.StateRunning,
		},
		"relay inspect": {
			func(d *docker.Fake) { d.ContainerStateErrs = map[string]error{RelayContainerName: errInjected} },
			"inspect relay", docker.StateStopped, docker.StateRunning,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := lanFixture(t, discovered("192.168.1.12"))
			saveLAN(t, f, "192.168.1.12", state.LANModePinned)
			d := f.app.docker.(*docker.Fake)
			lanForwarderPresent(t, f.app, d, lanState("192.168.1.12"))
			tc.inject(d)
			err := f.app.Stop()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := d.Containers[CaddyContainerName]; got != tc.caddyLeft {
				t.Errorf("caddy %v, want %v", got, tc.caddyLeft)
			}
			if got := d.Containers[LANContainerName]; got != tc.forwardLeft {
				t.Errorf("forwarder %v, want %v", got, tc.forwardLeft)
			}
		})
	}
}

// A Restart whose stop fails neither rediscovers the LAN address nor starts
// anything: the user sees the stop error with everything as it was.
func TestRestartStopFailureSkipsRediscoveryAndStart(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	before, _ := os.ReadFile(f.statePath)
	d := f.app.docker.(*docker.Fake)
	d.StopErr = errInjected
	if err := f.app.Restart(context.Background()); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v, want the stop failure", err)
	}
	if after, _ := os.ReadFile(f.statePath); string(before) != string(after) {
		t.Errorf("LAN address rediscovered after a failed stop:\n before %s\n after  %s", before, after)
	}
	if f.loads() != 0 {
		t.Errorf("registry pushed %d times", f.loads())
	}
}

// TestInstallFailuresBeforeRestore: every failure up to the shared network
// attach returns before the registry is pushed. Failures before the create
// pull and create nothing, and a failed create is rolled back even when the
// rollback itself fails, with the create error (the useful one) returned.
func TestInstallFailuresBeforeRestore(t *testing.T) {
	cases := []struct {
		name     string
		existing docker.ContainerState // StateAbsent: fresh install
		inject   func(d *docker.Fake)
		wantErr  string
		// wantCreate is whether a Caddy create was attempted.
		wantCreate bool
		wantPull   bool
	}{
		{
			name:    "inspect",
			inject:  func(d *docker.Fake) { d.ContainerStateErrs = map[string]error{CaddyContainerName: errInjected} },
			wantErr: "inspect container",
		},
		{
			name:     "drift inspect",
			existing: docker.StateRunning,
			inject:   func(d *docker.Fake) { d.InspectErr = errInjected },
			wantErr:  "inspect container",
		},
		{
			name:     "start stopped container",
			existing: docker.StateStopped,
			inject:   func(d *docker.Fake) { d.StartErr = errInjected },
			wantErr:  errInjected.Error(),
		},
		{
			name:     "attach started container",
			existing: docker.StateStopped,
			inject:   func(d *docker.Fake) { d.ConnectNetworkErr = errInjected },
			wantErr:  "attach shared network",
		},
		{
			name:    "volume",
			inject:  func(d *docker.Fake) { d.EnsureVolumeErr = errInjected },
			wantErr: errInjected.Error(),
		},
		{
			name:    "network",
			inject:  func(d *docker.Fake) { d.EnsureNetworkErr = errInjected },
			wantErr: errInjected.Error(),
		},
		{
			name:    "pull",
			inject:  func(d *docker.Fake) { d.PullErr = errInjected },
			wantErr: errInjected.Error(),
		},
		{
			name: "create and its rollback",
			inject: func(d *docker.Fake) {
				d.CreateAndStartErr = errInjected
				d.RemoveErr = errors.New("rm failed too")
			},
			wantErr:    errInjected.Error(),
			wantCreate: true,
			wantPull:   true,
		},
		{
			name:       "attach new container",
			inject:     func(d *docker.Fake) { d.ConnectNetworkErr = errInjected },
			wantErr:    "attach shared network",
			wantCreate: true,
			wantPull:   true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := docker.NewFake()
			if tc.existing != docker.StateAbsent {
				seedCurrentDesktopCaddy(fake, tc.existing)
			}
			f := newFixtureWith(t, http.StatusOK, fake)
			seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")
			tc.inject(fake)
			err := f.app.Install(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if strings.Contains(err.Error(), "rm failed too") {
				t.Errorf("rollback error replaced the create error: %v", err)
			}
			if f.loads() != 0 {
				t.Errorf("registry pushed %d times", f.loads())
			}
			if _, created := fake.Specs[CaddyContainerName]; created != tc.wantCreate {
				t.Errorf("caddy create attempted = %v, want %v", created, tc.wantCreate)
			}
			if fake.PulledImg[CaddyImage] != tc.wantPull {
				t.Errorf("image pulled = %v, want %v", fake.PulledImg[CaddyImage], tc.wantPull)
			}
			if tc.existing != docker.StateAbsent && len(fake.Removed) != 0 {
				t.Errorf("existing container removed: %v", fake.Removed)
			}
		})
	}
}

// TestRecreateFailuresAreContained: recreateCaddy reads state under the lock
// before touching the container, removes the old container only once it has
// inspected its networks, and pushes nothing until the new one is attached.
// Every failure releases the lock.
func TestRecreateFailuresAreContained(t *testing.T) {
	cases := []struct {
		name    string
		inject  func(t *testing.T, f *fixture, d *docker.Fake)
		wantErr string
		wantIs  error
		// wantRemoved is whether the old container was removed;
		// wantCreate whether a new one was attempted.
		wantRemoved, wantCreate bool
	}{
		{
			name:    "lock held",
			inject:  func(t *testing.T, f *fixture, _ *docker.Fake) { holdStateLock(t, f) },
			wantIs:  ErrLockContention,
			wantErr: "lock",
		},
		{
			name:    "inspect",
			inject:  func(_ *testing.T, _ *fixture, d *docker.Fake) { d.InspectErr = errInjected },
			wantErr: "inspect container",
		},
		{
			name:    "remove",
			inject:  func(_ *testing.T, _ *fixture, d *docker.Fake) { d.RemoveErr = errInjected },
			wantErr: errInjected.Error(),
		},
		{
			name:        "volume",
			inject:      func(_ *testing.T, _ *fixture, d *docker.Fake) { d.EnsureVolumeErr = errInjected },
			wantErr:     errInjected.Error(),
			wantRemoved: true,
		},
		{
			name:        "network",
			inject:      func(_ *testing.T, _ *fixture, d *docker.Fake) { d.EnsureNetworkErr = errInjected },
			wantErr:     errInjected.Error(),
			wantRemoved: true,
		},
		{
			name: "shared network",
			inject: func(_ *testing.T, _ *fixture, d *docker.Fake) {
				// Only the default bridge to reattach, so the shared
				// network attach is the first connect.
				info := d.Infos[CaddyContainerName]
				info.Networks = []string{"bridge"}
				d.Infos[CaddyContainerName] = info
				d.ConnectNetworkErr = errInjected
			},
			wantErr:     "attach shared network",
			wantRemoved: true,
			wantCreate:  true,
		},
		{
			name:        "certificate material",
			inject:      func(t *testing.T, f *fixture, _ *docker.Fake) { withUnreadableCert(t, f) },
			wantErr:     "recreated container but failed to load certificates",
			wantRemoved: true,
			wantCreate:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := docker.NewFake()
			seedCurrentDesktopCaddy(fake, docker.StateRunning)
			f := newFixtureWith(t, http.StatusOK, fake)
			seedState(t, f.statePath, "myapp", "myapp.test", "host.docker.internal:3000")
			tc.inject(t, f, fake)
			err := f.app.recreateCaddy(context.Background(), CaddyImage)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want %v", err, tc.wantIs)
			}
			if got := slices.Contains(fake.Removed, CaddyContainerName); got != tc.wantRemoved {
				t.Errorf("old container removed = %v, want %v", got, tc.wantRemoved)
			}
			if _, got := fake.Specs[CaddyContainerName]; got != tc.wantCreate {
				t.Errorf("new container created = %v, want %v", got, tc.wantCreate)
			}
			if f.loads() != 0 {
				t.Errorf("registry pushed %d times", f.loads())
			}
			if !errors.Is(tc.wantIs, ErrLockContention) {
				assertLockFree(t, f)
			}
		})
	}
}

// TestUpgradeFailuresLeaveContainer: upgrade decides whether to recreate
// only after the pull and both image lookups, so a failure in any of them
// leaves the running container alone.
func TestUpgradeFailuresLeaveContainer(t *testing.T) {
	cases := map[string]struct {
		inject  func(d *docker.Fake)
		wantErr string
	}{
		"inspect": {func(d *docker.Fake) {
			d.ContainerStateErrs = map[string]error{CaddyContainerName: errInjected}
		}, "inspect container"},
		"pull":               {func(d *docker.Fake) { d.PullErr = errInjected }, errInjected.Error()},
		"image id":           {func(d *docker.Fake) { d.ImageIDErr = errInjected }, "image id"},
		"container image id": {func(d *docker.Fake) { d.ContainerImageIDErr = errInjected }, "container image id"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newUpgradeFixture(t, http.StatusOK)
			d := f.app.docker.(*docker.Fake)
			d.ImageIDs[CaddyImage] = "sha256:new"
			d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
			tc.inject(d)
			_, err := f.app.Upgrade(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if errors.Is(err, ErrNotInstalled) {
				t.Errorf("a failed lookup read as not installed: %v", err)
			}
			if len(d.Removed) != 0 || len(d.Specs) != 0 {
				t.Errorf("container touched: removed=%v created=%v", d.Removed, d.Specs)
			}
		})
	}
}

// An up-to-date Caddy with a LAN forwarder still reports a forwarder
// problem, with the exit code of its cause.
func TestUpgradeAlreadyCurrentSurfacesLANFailure(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:same"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:same"
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	rep, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrStateCorrupt) || ExitCode(err) != 7 {
		t.Fatalf("err = %v (exit %d), want ErrStateCorrupt", err, ExitCode(err))
	}
	if rep.AlreadyCurrent {
		t.Error("a failed upgrade reported AlreadyCurrent")
	}
	if len(d.Removed) != 0 {
		t.Errorf("container removed: %v", d.Removed)
	}
}

// After a recreate, a forwarder problem is still reported, but Caddy is
// already serving the restored registry.
func TestUpgradeRecreateThenLANFailure(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	f.app.WithLANResolver(stubLAN{present: true})
	saveLAN(t, f, "192.168.1.12", state.LANModePinned)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
	d.ContainerStateErrs = map[string]error{LANContainerName: errInjected}
	_, err := f.app.Upgrade(context.Background())
	if err == nil || !strings.Contains(err.Error(), "inspect LAN forwarder") {
		t.Fatalf("err = %v, want the forwarder inspect failure", err)
	}
	if !slices.Contains(d.Removed, CaddyContainerName) || !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("caddy not recreated and restored before the LAN step: removed=%v body=%s", d.Removed, f.lastBody)
	}
}

// Ctrl+C during the admin wait returns at once with the context's error.
func TestWaitForAdminHonoursCancellation(t *testing.T) {
	saved := adminReadyTimeout
	adminReadyTimeout = time.Minute
	t.Cleanup(func() { adminReadyTimeout = saved })
	a := noAdminApp(t, docker.NewFake())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if err := a.waitForAdmin(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waitForAdmin took %s after cancellation", time.Since(start))
	}
}

// TestEnsureRelayFailures: on native Linux each relay failure is surfaced
// (Start and Install report it next to restored routes), an existing relay
// is only replaced once its removal succeeds, and a failed create is
// cleaned up.
func TestEnsureRelayFailures(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(d *docker.Fake)
		inject  func(d *docker.Fake)
		wantErr string
		// wantLeft is the relay state the failure leaves behind.
		wantLeft docker.ContainerState
	}{
		{
			name:     "inspect",
			inject:   func(d *docker.Fake) { d.ContainerStateErrs = map[string]error{RelayContainerName: errInjected} },
			wantErr:  "inspect relay",
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "inspect existing",
			seed:     func(d *docker.Fake) { _ = d.CreateAndStart(relayContainerSpec(DefaultInstance(), CaddyImage)) },
			inject:   func(d *docker.Fake) { d.InspectErr = errInjected },
			wantErr:  "inspect relay",
			wantLeft: docker.StateRunning,
		},
		{
			name: "start stopped",
			seed: func(d *docker.Fake) {
				_ = d.CreateAndStart(relayContainerSpec(DefaultInstance(), CaddyImage))
				d.Containers[RelayContainerName] = docker.StateStopped
			},
			inject:   func(d *docker.Fake) { d.StartErr = errInjected },
			wantErr:  errInjected.Error(),
			wantLeft: docker.StateStopped,
		},
		{
			name: "remove drifted",
			seed: func(d *docker.Fake) {
				d.Containers[RelayContainerName] = docker.StateRunning
				d.Infos[RelayContainerName] = docker.ContainerInfo{NetworkMode: "bridge"}
			},
			inject:   func(d *docker.Fake) { d.RemoveErr = errInjected },
			wantErr:  errInjected.Error(),
			wantLeft: docker.StateRunning,
		},
		{
			name:     "volume",
			inject:   func(d *docker.Fake) { d.EnsureVolumeErr = errInjected },
			wantErr:  errInjected.Error(),
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "create",
			inject:   func(d *docker.Fake) { d.CreateAndStartErr = errInjected },
			wantErr:  "start relay",
			wantLeft: docker.StateAbsent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := docker.NewFake()
			useNativeLinux(t, d)
			a := noAdminApp(t, d)
			if tc.seed != nil {
				tc.seed(d)
			}
			delete(d.Specs, RelayContainerName)
			tc.inject(d)
			err := a.ensureRelay(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			got, ok := d.Containers[RelayContainerName]
			if !ok {
				got = docker.StateAbsent
			}
			if got != tc.wantLeft {
				t.Errorf("relay left %v, want %v", got, tc.wantLeft)
			}
			if tc.name != "create" {
				if _, created := d.Specs[RelayContainerName]; created {
					t.Error("relay create attempted")
				}
			}
		})
	}
}
