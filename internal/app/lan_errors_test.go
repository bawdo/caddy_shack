package app

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// lanForwarderPresent creates the forwarder for s on a, so a test starts
// from a forwarder that matches its settings, and clears the call record.
func lanForwarderPresent(t *testing.T, a *App, fake *docker.Fake, s *state.State) {
	t.Helper()
	if err := a.ensureLAN(s); err != nil {
		t.Fatal(err)
	}
	fake.Removed = nil
	delete(fake.Specs, LANContainerName)
}

// TestEnsureLANFailures: every failure before the create leaves an existing
// forwarder alone (a stale one is still better than none), and a failure of
// the create itself removes the half-made container so a retry starts clean.
func TestEnsureLANFailures(t *testing.T) {
	const ip = "192.168.1.12"
	cases := []struct {
		name string
		// seed puts a forwarder in place before the injected failure.
		seed     func(t *testing.T, a *App, fake *docker.Fake)
		resolver LANResolver
		inject   func(fake *docker.Fake)
		s        *state.State
		wantErr  string
		// wantStart is true for failures reported as a forwarder that could
		// not start, which start, sync and friends turn into a notice.
		wantStart bool
		// wantLeft is the forwarder state the failure must leave behind.
		wantLeft docker.ContainerState
	}{
		{
			name:     "invalid stored IP touches nothing",
			resolver: stubLAN{present: true},
			s:        lanState("not-an-ip"),
			wantErr:  `LAN IP "not-an-ip" in state is invalid`,
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "presence check failure touches nothing",
			resolver: stubLAN{err: errors.New("netlink: boom")},
			s:        lanState(ip),
			wantErr:  "check LAN IP 192.168.1.12: netlink: boom",
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "network failure creates nothing",
			resolver: stubLAN{present: true},
			inject:   func(fake *docker.Fake) { fake.EnsureNetworkErr = errors.New("network create refused") },
			s:        lanState(ip),
			wantErr:  "network create refused",
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "inspect failure creates nothing",
			resolver: stubLAN{present: true},
			inject: func(fake *docker.Fake) {
				fake.ContainerStateErrs = map[string]error{LANContainerName: errors.New("inspect refused")}
			},
			s:        lanState(ip),
			wantErr:  "inspect LAN forwarder: inspect refused",
			wantLeft: docker.StateAbsent,
		},
		{
			name:     "inspect failure leaves an existing forwarder",
			seed:     func(t *testing.T, a *App, fake *docker.Fake) { lanForwarderPresent(t, a, fake, lanState(ip)) },
			resolver: stubLAN{present: true},
			inject:   func(fake *docker.Fake) { fake.InspectErr = errors.New("inspect refused") },
			s:        lanState("192.168.1.99"),
			wantErr:  "inspect LAN forwarder: inspect refused",
			wantLeft: docker.StateRunning,
		},
		{
			name: "start failure of a stopped forwarder keeps it",
			seed: func(t *testing.T, a *App, fake *docker.Fake) {
				lanForwarderPresent(t, a, fake, lanState(ip))
				fake.Containers[LANContainerName] = docker.StateStopped
			},
			resolver:  stubLAN{present: true},
			inject:    func(fake *docker.Fake) { fake.StartErr = errors.New("start refused") },
			s:         lanState(ip),
			wantErr:   "could not start the LAN forwarder on 192.168.1.12:443 and 192.168.1.12:80: start refused",
			wantStart: true,
			wantLeft:  docker.StateStopped,
		},
		{
			name:     "remove failure of a drifted forwarder creates nothing",
			seed:     func(t *testing.T, a *App, fake *docker.Fake) { lanForwarderPresent(t, a, fake, lanState(ip)) },
			resolver: stubLAN{present: true},
			inject:   func(fake *docker.Fake) { fake.RemoveErr = errors.New("rm refused") },
			s:        lanState("192.168.1.99"),
			wantErr:  "rm refused",
			wantLeft: docker.StateRunning,
		},
		{
			name:      "create failure removes the half-made forwarder",
			resolver:  stubLAN{present: true},
			inject:    func(fake *docker.Fake) { fake.CreateAndStartErr = errors.New("driver failed") },
			s:         lanState(ip),
			wantErr:   "could not start the LAN forwarder on 192.168.1.12:443 and 192.168.1.12:80: driver failed",
			wantStart: true,
			wantLeft:  docker.StateAbsent,
		},
		{
			name:     "address in use on the HTTP port names that port",
			resolver: stubLAN{present: true},
			inject: func(fake *docker.Fake) {
				fake.CreateAndStartErr = errors.New("Bind for 192.168.1.12:80 failed: port is already allocated")
			},
			s:         lanState(ip),
			wantErr:   "192.168.1.12:80 is already in use by another process",
			wantStart: true,
			wantLeft:  docker.StateAbsent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, fake := newLANApp(true)
			if tc.seed != nil {
				tc.seed(t, a, fake)
			}
			a.WithLANResolver(tc.resolver)
			if tc.inject != nil {
				tc.inject(fake)
			}
			err := a.ensureLAN(tc.s)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if errors.Is(err, ErrLANDown) {
				t.Errorf("err = %v must not read as LAN down", err)
			}
			var startFail *lanStartFailure
			if got := errors.As(err, &startFail); got != tc.wantStart {
				t.Errorf("lanStartFailure = %v, want %v (decides notice versus error)", got, tc.wantStart)
			}
			got, ok := fake.Containers[LANContainerName]
			if !ok {
				got = docker.StateAbsent
			}
			if got != tc.wantLeft {
				t.Errorf("forwarder left %v, want %v", got, tc.wantLeft)
			}
		})
	}
}

func TestStopLANAndRemoveLANSurfaceInspectFailure(t *testing.T) {
	a, fake := newLANApp(true)
	fake.Containers[LANContainerName] = docker.StateRunning
	fake.ContainerStateErrs = map[string]error{LANContainerName: errors.New("inspect refused")}
	for name, fn := range map[string]func() error{"stopLAN": a.stopLAN, "removeLAN": a.removeLAN} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "inspect LAN forwarder: inspect refused") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if fake.Containers[LANContainerName] != docker.StateRunning || len(fake.Removed) != 0 {
		t.Errorf("forwarder changed: %v removed=%v", fake.Containers[LANContainerName], fake.Removed)
	}
}

// A changed address that cannot be saved must not be announced, and the
// stored address stays as it was.
func TestRediscoverLANSaveFailureKeepsStateAndSaysNothing(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	saveLAN(t, f, "192.168.1.12", state.LANModeDiscovered)
	// The lock file must exist before the directory goes read-only.
	if l, err := state.AcquireLock(f.statePath+".lock", time.Second); err != nil {
		t.Fatal(err)
	} else {
		_ = l.Release()
	}
	before, _ := os.ReadFile(f.statePath)
	readOnlyStateDir(t, f.statePath)
	buf := notices(f)
	err := f.app.rediscoverLAN()
	if err == nil || !strings.Contains(err.Error(), "open tmp") {
		t.Fatalf("err = %v, want the state write failure", err)
	}
	if after, _ := os.ReadFile(f.statePath); !bytes.Equal(before, after) {
		t.Errorf("state changed:\n before %s\n after  %s", before, after)
	}
	if strings.Contains(buf.String(), "LAN IP changed") {
		t.Errorf("unsaved change announced: %q", buf.String())
	}
}

func TestRediscoverLANCorruptStateIsStateCorrupt(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.50"))
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.rediscoverLAN(); !errors.Is(err, ErrStateCorrupt) || ExitCode(err) != 7 {
		t.Fatalf("err = %v (exit %d), want ErrStateCorrupt", err, ExitCode(err))
	}
	if held, _ := state.LockHeld(f.statePath + ".lock"); held {
		t.Error("state lock still held after the failure")
	}
}

// TestUpgradeLANFailuresLeaveForwarder: upgradeLAN only replaces the
// forwarder once it knows the new image differs, so every failure before
// that point leaves the running forwarder serving.
func TestUpgradeLANFailuresLeaveForwarder(t *testing.T) {
	cases := map[string]struct {
		inject  func(d *docker.Fake)
		wantErr string
	}{
		"pull":            {func(d *docker.Fake) { d.PullErr = errors.New("registry down") }, "registry down"},
		"inspect":         {func(d *docker.Fake) { d.ContainerStateErrs = map[string]error{LANContainerName: errors.New("x")} }, "inspect LAN forwarder"},
		"image id":        {func(d *docker.Fake) { d.ImageIDErr = errors.New("x") }, "image id: x"},
		"container image": {func(d *docker.Fake) { d.ContainerImageIDErr = errors.New("x") }, "container image id: x"},
		"remove stale":    {func(d *docker.Fake) { d.RemoveErr = errors.New("rm refused") }, "rm refused"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, d := upgradeLANFixture(t)
			d.ContainerImageIDs[LANContainerName] = "sha256:old"
			delete(d.Specs, LANContainerName)
			tc.inject(d)
			err := f.app.upgradeLAN()
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			d.ContainerStateErrs = nil
			if st, _ := d.ContainerState(LANContainerName); st != docker.StateRunning || len(d.Removed) != 0 {
				t.Errorf("forwarder %v removed=%v, want it left running", st, d.Removed)
			}
			if _, ok := d.Specs[LANContainerName]; ok {
				t.Error("a replacement forwarder was created")
			}
		})
	}
}

func TestUpgradeLANCorruptStateIsStateCorrupt(t *testing.T) {
	f, d := upgradeLANFixture(t)
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.upgradeLAN(); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v, want ErrStateCorrupt", err)
	}
	if len(d.PulledImg) != 0 || len(d.Removed) != 0 {
		t.Errorf("docker touched: pulled=%v removed=%v", d.PulledImg, d.Removed)
	}
}

// TestLANEnableFailuresBeforeCommitChangeNothing: a failure up to and
// including the Caddy push writes no state, binds no forwarder, and maps to
// the exit code a script can act on. The race cases change state.json while
// the forwarder image is pulled, which is the window between the lock-free
// peek and the read under the lock.
func TestLANEnableFailuresBeforeCommitChangeNothing(t *testing.T) {
	cases := []struct {
		name        string
		caddyStatus int
		seed        func(t *testing.T, f *fixture)
		duringPull  func(t *testing.T, f *fixture)
		in          LANEnableInput
		wantIs      error
		wantExit    int
	}{
		{
			name: "corrupt state",
			seed: func(t *testing.T, f *fixture) {
				if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			in:       LANEnableInput{Suffix: "lan"},
			wantIs:   ErrStateCorrupt,
			wantExit: 7,
		},
		{
			name: "lock taken during the pull",
			duringPull: func(t *testing.T, f *fixture) {
				shortLockTimeout(t)
				l, err := state.AcquireLock(f.statePath+".lock", time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			},
			in:       LANEnableInput{Suffix: "lan"},
			wantIs:   ErrLockContention,
			wantExit: 8,
		},
		{
			name: "state corrupted during the pull",
			duringPull: func(t *testing.T, f *fixture) {
				if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			in:       LANEnableInput{Suffix: "lan"},
			wantIs:   ErrStateCorrupt,
			wantExit: 7,
		},
		{
			name: "stored suffix gone during the pull",
			seed: func(t *testing.T, f *fixture) {
				saveState(t, f.statePath, &state.State{LAN: &state.LAN{Suffix: "kept.lan"}})
			},
			duringPull: func(t *testing.T, f *fixture) {
				saveState(t, f.statePath, &state.State{})
			},
			in:       LANEnableInput{},
			wantIs:   ErrInvalidArgs,
			wantExit: 2,
		},
		{
			name:        "caddy rejects the config",
			caddyStatus: http.StatusBadRequest,
			in:          LANEnableInput{Suffix: "lan"},
			wantExit:    1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.caddyStatus
			if status == 0 {
				status = http.StatusOK
			}
			f := newFixture(t, status)
			f.app.WithLANResolver(discovered("192.168.1.12"))
			if tc.seed != nil {
				tc.seed(t, f)
			}
			fake := f.app.docker.(*docker.Fake)
			rec := &recDocker{Client: fake}
			f.app.docker = rec
			if tc.duringPull != nil {
				rec.onPull = func(string) { tc.duringPull(t, f) }
			}
			before, _ := os.ReadFile(f.statePath)

			_, err := f.app.LANEnable(context.Background(), tc.in)
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want %v", err, tc.wantIs)
			}
			if got := ExitCode(err); got != tc.wantExit {
				t.Errorf("exit = %d, want %d (err %v)", got, tc.wantExit, err)
			}
			after, _ := os.ReadFile(f.statePath)
			if tc.duringPull == nil && !bytes.Equal(before, after) {
				t.Errorf("state written:\n before %s\n after  %s", before, after)
			}
			if s, lerr := state.Load(f.statePath); lerr == nil && s.LANEnabled() {
				t.Error("state records LAN enabled after a failed enable")
			}
			if slices.Contains(rec.calls, "create:"+LANContainerName) {
				t.Errorf("forwarder created: %v", rec.calls)
			}
			if held, _ := state.LockHeld(f.statePath + ".lock"); held && !errors.Is(tc.wantIs, ErrLockContention) {
				t.Error("state lock still held after the failure")
			}
		})
	}
}

// Once the config is pushed and saved, a failure to attach the shared
// network is reported, the saved settings stand (the next start or sync
// binds them), and no forwarder is created on a network Caddy is not on.
func TestLANEnableSharedNetworkFailureAfterCommit(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	d := f.app.docker.(*docker.Fake)
	d.ConnectNetworkErr = errors.New("connect refused")
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{Suffix: "lan"})
	if err == nil || !strings.Contains(err.Error(), "ensure shared network: connect refused") {
		t.Fatalf("err = %v", err)
	}
	if s, _ := state.Load(f.statePath); !s.LANEnabled() {
		t.Error("committed settings were not kept")
	}
	if _, ok := d.Specs[LANContainerName]; ok {
		t.Error("forwarder created although the network attach failed")
	}
}

func TestLANEnablePinnedPresenceCheckFailure(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	f.app.WithLANResolver(stubLAN{err: errors.New("netlink: boom")})
	_, err := f.app.LANEnable(context.Background(), LANEnableInput{IP: "192.168.1.50", Suffix: "lan"})
	if err == nil || !strings.Contains(err.Error(), "check LAN IP 192.168.1.50: netlink: boom") {
		t.Fatalf("err = %v", err)
	}
	if errors.Is(err, ErrInvalidArgs) {
		t.Error("a failed interface lookup is not the user's bad argument")
	}
	if _, serr := os.Stat(f.statePath); serr == nil {
		t.Error("state was written")
	}
}

// TestLANDisableFailuresKeepLANEnabled: LANDisable removes the forwarder
// before it records anything, so any failure up to and including the push
// leaves state saying LAN is on, and the next disable tries again.
func TestLANDisableFailuresKeepLANEnabled(t *testing.T) {
	cases := []struct {
		name          string
		caddyStatus   int
		inject        func(t *testing.T, f *fixture, d *docker.Fake)
		wantIs        error
		wantForwarder bool
	}{
		{
			name: "lock held",
			inject: func(t *testing.T, f *fixture, _ *docker.Fake) {
				shortLockTimeout(t)
				l, err := state.AcquireLock(f.statePath+".lock", time.Second)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = l.Release() })
			},
			wantIs:        ErrLockContention,
			wantForwarder: true,
		},
		{
			name: "forwarder remove refused",
			inject: func(_ *testing.T, _ *fixture, d *docker.Fake) {
				d.RemoveErr = errors.New("rm refused")
			},
			wantForwarder: true,
		},
		{
			name:        "caddy rejects the config",
			caddyStatus: http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.caddyStatus
			if status == 0 {
				status = http.StatusOK
			}
			f := newFixture(t, status)
			f.app.WithLANResolver(stubLAN{present: true})
			d := f.app.docker.(*docker.Fake)
			saveLAN(t, f, "192.168.1.12", state.LANModePinned)
			lanForwarderPresent(t, f.app, d, lanState("192.168.1.12"))
			if tc.inject != nil {
				tc.inject(t, f, d)
			}
			err := f.app.LANDisable(context.Background())
			if err == nil {
				t.Fatal("want an error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("err = %v, want %v", err, tc.wantIs)
			}
			if s, _ := state.Load(f.statePath); !s.LANEnabled() {
				t.Error("state says LAN is off although the disable failed")
			}
			if _, ok := d.Containers[LANContainerName]; ok != tc.wantForwarder {
				t.Errorf("forwarder present = %v, want %v", ok, tc.wantForwarder)
			}
		})
	}
}

// A corrupt state.json stops LANDisable before Docker is touched: the
// forwarder keeps serving until the file is fixed.
func TestLANDisableCorruptStateLeavesForwarder(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	d := f.app.docker.(*docker.Fake)
	lanForwarderPresent(t, f.app, d, lanState("192.168.1.12"))
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.app.LANDisable(context.Background()); !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("err = %v, want ErrStateCorrupt", err)
	}
	if d.Containers[LANContainerName] != docker.StateRunning {
		t.Error("forwarder removed on a corrupt state")
	}
}

// With LAN never enabled, disable still clears a stray forwarder but writes
// no state and pushes nothing.
func TestLANDisableWithoutLANOnlyRemovesStrayForwarder(t *testing.T) {
	f := lanFixture(t, discovered("192.168.1.12"))
	d := f.app.docker.(*docker.Fake)
	d.Containers[LANContainerName] = docker.StateRunning
	if err := f.app.LANDisable(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := d.Containers[LANContainerName]; ok {
		t.Error("stray forwarder left running")
	}
	if _, err := os.Stat(f.statePath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("state written: %v", err)
	}
	if f.loads() != 0 {
		t.Errorf("caddy pushed %d times", f.loads())
	}
}
