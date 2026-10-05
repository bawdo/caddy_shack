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

// nativeFixture is newFixture on a native Linux engine, with notices
// captured and two host-mode backends plus one container-mode backend in
// state.
func nativeFixture(t *testing.T, caddyStatus int) (*fixture, *docker.Fake, *bytes.Buffer) {
	t.Helper()
	f := newFixture(t, caddyStatus)
	fake := f.app.docker.(*docker.Fake)
	useNativeLinux(t, fake)
	var notices bytes.Buffer
	f.app.notices = &notices
	seed := &state.State{SchemaVersion: state.SchemaVersion, Backends: map[string]state.Backend{
		"mailpit":     {Domain: "mailpit.test", Upstream: "host.docker.internal:8025", RegisteredAt: "2026-10-05T00:00:00Z"},
		"grafana":     {Domain: "grafana.test", Upstream: "host.docker.internal:3000", RegisteredAt: "2026-10-05T00:00:00Z"},
		"arrix-gamma": {Domain: "gamma.arrix.dev", Upstream: "arrix-gamma:80", RegisteredAt: "2026-10-05T00:00:00Z"},
	}}
	if err := state.Save(f.statePath, seed); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return f, fake, &notices
}

// seedLegacyCaddy models a caddy_shack container created before native
// Linux support: bridge mode, no extra hosts, data+config volumes only,
// attached to the default bridge, the shared network, and one more network
// another tool connected it to.
func seedLegacyCaddy(fake *docker.Fake, st docker.ContainerState) {
	fake.Containers[CaddyContainerName] = st
	fake.Infos[CaddyContainerName] = docker.ContainerInfo{
		NetworkMode: "bridge",
		Mounts: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		Networks: []string{"arrix-gamma", "bridge", CaddyNetworkName},
	}
}

// seedCurrentCaddy models a caddy_shack container already created with the
// native Linux settings, plus a running relay.
func seedCurrentCaddy(fake *docker.Fake) {
	hn := HostNetwork{Kind: EngineNativeLinux, Gateway: testGateway}
	_ = fake.CreateAndStart(caddyContainerSpec(CaddyImage, hn))
	_ = fake.ConnectNetwork(CaddyNetworkName, CaddyContainerName)
	_ = fake.CreateAndStart(relayContainerSpec(CaddyImage))
	fake.Removed = nil
}

func assertRecreatedForNative(t *testing.T, f *fixture, fake *docker.Fake, before []byte) {
	t.Helper()
	if !slices.Contains(fake.Removed, CaddyContainerName) {
		t.Fatalf("caddy_shack was not recreated; removed=%v", fake.Removed)
	}
	spec := fake.Specs[CaddyContainerName]
	if !slices.Contains(spec.ExtraHosts, HostGatewayEntry) {
		t.Errorf("recreated without host-gateway: %+v", spec.ExtraHosts)
	}
	for _, v := range []string{CaddyDataVolume, CaddyConfigVolume, RelayVolume} {
		if !fake.Volumes[v] {
			t.Errorf("volume %s not ensured", v)
		}
		if !slices.ContainsFunc(spec.Volumes, func(m docker.VolumeMount) bool { return m.VolumeName == v }) {
			t.Errorf("recreated container does not mount %s (CA/certs/config must be kept): %+v", v, spec.Volumes)
		}
	}
	info, _ := fake.InspectContainer(CaddyContainerName)
	if got := strings.Join(info.Networks, ","); got != "arrix-gamma,bridge,caddy_shack" {
		t.Errorf("networks after recreate = %q, want every original network reattached", got)
	}
	if st, _ := fake.ContainerState(RelayContainerName); st != docker.StateRunning {
		t.Errorf("relay not running after recreate: %v", st)
	}
	body := string(f.lastBody)
	if !strings.Contains(body, "unix//run/caddy_shack/relay.sock") || !strings.Contains(body, "gamma.arrix.dev") {
		t.Errorf("backends not restored through the relay:\n%s", body)
	}
	after, _ := os.ReadFile(f.statePath)
	if !bytes.Equal(before, after) {
		t.Errorf("state.json must not change on recreate:\n before %s\n after  %s", before, after)
	}
}

func TestInstallRecreatesLegacyContainerOnNativeLinux(t *testing.T) {
	for _, st := range []docker.ContainerState{docker.StateRunning, docker.StateStopped} {
		t.Run(st.String(), func(t *testing.T) {
			f, fake, notices := nativeFixture(t, http.StatusOK)
			seedLegacyCaddy(fake, st)
			before, _ := os.ReadFile(f.statePath)

			if err := f.app.Install(context.Background()); err != nil {
				t.Fatalf("Install: %v", err)
			}
			assertRecreatedForNative(t, f, fake, before)
			if fake.PulledImg[CaddyImage] {
				t.Error("recreate should reuse the local image, not pull")
			}
			if !strings.Contains(notices.String(), "recreating caddy_shack") {
				t.Errorf("notices = %q", notices.String())
			}
		})
	}
}

func TestInstallLeavesCurrentNativeContainerAlone(t *testing.T) {
	f, fake, notices := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	if err := f.app.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(fake.Removed) != 0 {
		t.Errorf("up-to-date container must not be recreated: removed=%v", fake.Removed)
	}
	if notices.Len() != 0 {
		t.Errorf("unexpected notices: %q", notices.String())
	}
}

func TestInstallNativeRestartsStoppedRelay(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	fake.Containers[RelayContainerName] = docker.StateStopped
	if err := f.app.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if st, _ := fake.ContainerState(RelayContainerName); st != docker.StateRunning {
		t.Errorf("relay state: %v", st)
	}
	if slices.Contains(fake.Removed, RelayContainerName) {
		t.Error("a matching relay should be started, not replaced")
	}
}

func TestInstallNativeReplacesMisconfiguredRelay(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	fake.Infos[RelayContainerName] = docker.ContainerInfo{NetworkMode: "bridge"}
	if err := f.app.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !slices.Contains(fake.Removed, RelayContainerName) {
		t.Error("misconfigured relay should be replaced")
	}
	if fake.Specs[RelayContainerName].NetworkMode != "host" {
		t.Errorf("relay spec: %+v", fake.Specs[RelayContainerName])
	}
}

func TestInstallFreshNativeCreatesCaddyAndRelay(t *testing.T) {
	fake := docker.NewFake()
	useNativeLinux(t, fake)
	a := NewWithPaths(Paths{}, nil, fake)
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !slices.Contains(fake.Specs[CaddyContainerName].ExtraHosts, HostGatewayEntry) {
		t.Errorf("caddy spec: %+v", fake.Specs[CaddyContainerName])
	}
	if fake.Specs[RelayContainerName].NetworkMode != "host" || !fake.Volumes[RelayVolume] {
		t.Errorf("relay not created: %+v", fake.Specs[RelayContainerName])
	}
}

// TestInstallDesktopNeverInspectsOrRecreates pins the macOS behaviour: a
// running container is left alone without even being inspected, and no
// relay is created.
func TestInstallDesktopNeverInspectsOrRecreates(t *testing.T) {
	fake := docker.NewFake()
	fake.InspectErr = errors.New("desktop install must not inspect the container")
	seedLegacyCaddy(fake, docker.StateRunning)
	a := NewWithPaths(Paths{}, nil, fake)
	var notices bytes.Buffer
	a.notices = &notices
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if len(fake.Removed) != 0 || len(fake.Specs) != 0 {
		t.Errorf("desktop install touched containers: removed=%v specs=%v", fake.Removed, fake.Specs)
	}
	if notices.Len() != 0 {
		t.Errorf("desktop install should be quiet: %q", notices.String())
	}
}

func TestInstallFreshUnknownEngineKeepsDesktopSpecAndSaysSo(t *testing.T) {
	fake := docker.NewFake()
	fake.Engine = docker.EngineInfo{OSType: "linux", OperatingSystem: "Ubuntu", Name: "box"}
	stubInterfaces(t, nil)
	a := NewWithPaths(Paths{}, nil, fake)
	var notices bytes.Buffer
	a.notices = &notices
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := strings.Join(docker.RunArgs(fake.Specs[CaddyContainerName]), "\x00"); got != strings.Join(desktopRunArgs, "\x00") {
		t.Errorf("unknown engine should get the Desktop args, got %q", docker.RunArgs(fake.Specs[CaddyContainerName]))
	}
	if _, ok := fake.Specs[RelayContainerName]; ok {
		t.Error("unknown engine must not get a relay")
	}
	if !strings.Contains(notices.String(), "could not classify the docker engine") {
		t.Errorf("missing unknown-engine notice: %q", notices.String())
	}
}

func TestRecreateAbortsOnCorruptStateWithoutTouchingContainer(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedLegacyCaddy(fake, docker.StateRunning)
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := f.app.Install(context.Background())
	if !errors.Is(err, ErrStateCorrupt) {
		t.Fatalf("want ErrStateCorrupt, got %v", err)
	}
	if len(fake.Removed) != 0 {
		t.Errorf("container removed despite corrupt state: %v", fake.Removed)
	}
}

func TestRecreateFailures(t *testing.T) {
	cases := map[string]struct {
		setup       func(fake *docker.Fake)
		caddyStatus int
	}{
		"create":    {func(fake *docker.Fake) { fake.CreateAndStartErr = errors.New("run") }, http.StatusOK},
		"reconnect": {func(fake *docker.Fake) { fake.ConnectNetworkErr = errors.New("connect") }, http.StatusOK},
		"restore":   {func(*docker.Fake) {}, http.StatusInternalServerError},
	}
	savedTimeout, savedInterval := adminReadyTimeout, adminReadyInterval
	adminReadyTimeout, adminReadyInterval = 50*time.Millisecond, 10*time.Millisecond
	defer func() { adminReadyTimeout, adminReadyInterval = savedTimeout, savedInterval }()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f, fake, _ := nativeFixture(t, tc.caddyStatus)
			seedLegacyCaddy(fake, docker.StateRunning)
			tc.setup(fake)
			if err := f.app.Install(context.Background()); err == nil {
				t.Error("expected error")
			}
		})
	}
}

// TestRecreateRestoresBackendsBeforeRelay: a relay failure must not leave
// Caddy without routes (a re-run of install would then see an up-to-date
// container and never restore them).
func TestRecreateRestoresBackendsBeforeRelay(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedLegacyCaddy(fake, docker.StateRunning)
	fake.CreateErrs = map[string]error{RelayContainerName: errors.New("relay boom")}
	err := f.app.Install(context.Background())
	if err == nil || !strings.Contains(err.Error(), "restored backends, but the relay failed") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(string(f.lastBody), "gamma.arrix.dev") {
		t.Errorf("backends were not restored before the relay step:\n%s", f.lastBody)
	}
}

func TestStartNotesLegacyContainerButNeverRecreates(t *testing.T) {
	for _, st := range []docker.ContainerState{docker.StateRunning, docker.StateStopped} {
		t.Run(st.String(), func(t *testing.T) {
			f, fake, notices := nativeFixture(t, http.StatusOK)
			seedLegacyCaddy(fake, st)
			if err := f.app.Start(context.Background()); err != nil {
				t.Fatalf("Start: %v", err)
			}
			if slices.Contains(fake.Removed, CaddyContainerName) {
				t.Errorf("start must never recreate: removed=%v", fake.Removed)
			}
			if !strings.Contains(notices.String(), "Run: caddy_shack install") {
				t.Errorf("missing install hint: %q", notices.String())
			}
			if rs, _ := fake.ContainerState(RelayContainerName); rs != docker.StateRunning {
				t.Errorf("start should bring the relay up: %v", rs)
			}
		})
	}
}

func TestStopAlsoStopsRelay(t *testing.T) {
	_, fake, _ := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	a := NewWithPaths(Paths{}, nil, fake)
	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	for _, n := range []string{CaddyContainerName, RelayContainerName} {
		if st, _ := fake.ContainerState(n); st != docker.StateStopped {
			t.Errorf("%s: %v", n, st)
		}
	}
}

func TestRegisterHostModeOnNativeRoutesViaRelay(t *testing.T) {
	f, fake, notices := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "vite", Port: 5173}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	body := string(f.lastBody)
	if !strings.Contains(body, `"5173"`) || !strings.Contains(body, "relay.sock") {
		t.Errorf("vite.test should route via the relay with port 5173:\n%s", body)
	}
	s, _ := state.Load(f.statePath)
	if s.Backends["vite"].Upstream != "host.docker.internal:5173" {
		t.Errorf("state keeps the engine-neutral upstream, got %q", s.Backends["vite"].Upstream)
	}
	if notices.Len() != 0 {
		t.Errorf("current container should not produce notices: %q", notices.String())
	}
}

func TestRegisterHostModeOnLegacyNativeContainerHints(t *testing.T) {
	f, fake, notices := nativeFixture(t, http.StatusOK)
	seedLegacyCaddy(fake, docker.StateRunning)
	if err := f.app.Register(context.Background(), RegisterInput{Name: "vite", Port: 5173}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !strings.Contains(notices.String(), "Run: caddy_shack install") {
		t.Errorf("missing install hint: %q", notices.String())
	}
	if len(fake.Removed) != 0 {
		t.Error("register must never recreate")
	}
}

func TestUpgradeReattachesEveryNetwork(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(map[bool]string{false: "desktop", true: "native"}[native], func(t *testing.T) {
			f := newUpgradeFixture(t, http.StatusOK)
			fake := f.app.docker.(*docker.Fake)
			if native {
				useNativeLinux(t, fake)
			}
			seedLegacyCaddy(fake, docker.StateRunning)
			fake.ImageIDs[CaddyImage] = "sha256:new"
			fake.ContainerImageIDs[CaddyContainerName] = "sha256:old"
			if _, err := f.app.Upgrade(context.Background()); err != nil {
				t.Fatalf("Upgrade: %v", err)
			}
			info, _ := fake.InspectContainer(CaddyContainerName)
			if got := strings.Join(info.Networks, ","); got != "arrix-gamma,bridge,caddy_shack" {
				t.Errorf("networks after upgrade = %q", got)
			}
			_, hasRelay := fake.Specs[RelayContainerName]
			if hasRelay != native {
				t.Errorf("relay created = %v, want %v", hasRelay, native)
			}
		})
	}
}

func TestStatusNativeRequiresRelay(t *testing.T) {
	f, fake, _ := nativeFixture(t, http.StatusOK)
	seedCurrentCaddy(fake)
	fake.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{Stdout: []byte("v2.8.4")}
	rep, err := f.app.Status(context.Background())
	if err != nil || !rep.Healthy || rep.RelayState != "running" {
		t.Fatalf("status = %+v, %v", rep, err)
	}
	fake.Containers[RelayContainerName] = docker.StateStopped
	f.app.hn = nil
	rep, err = f.app.Status(context.Background())
	if !errors.Is(err, ErrUnhealthy) || rep.Healthy || rep.RelayState != "stopped" {
		t.Errorf("stopped relay should be unhealthy: %+v, %v", rep, err)
	}
}

func TestStatusDesktopHasNoRelayState(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	rep, _ := f.app.Status(context.Background())
	if rep.RelayState != "" {
		t.Errorf("desktop status should not report a relay: %+v", rep)
	}
}
