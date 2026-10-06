package app

import (
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

const testGateway = "172.17.0.1"

var (
	nativeEngine = docker.EngineInfo{OperatingSystem: "Omarchy", OSType: "linux", Name: "worklappy"}
	hostIPs      = []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.20"), net.ParseIP(testGateway)}
)

// stubInterfaces points the host-interface seam at ips for one test.
func stubInterfaces(t *testing.T, ips []net.IP) {
	t.Helper()
	orig := interfaceAddrs
	interfaceAddrs = func() ([]net.IP, error) { return ips, nil }
	t.Cleanup(func() { interfaceAddrs = orig })
}

// useNativeLinux makes fake report a native Linux engine whose bridge
// gateway is on a (stubbed) host interface.
func useNativeLinux(t *testing.T, fake *docker.Fake) {
	t.Helper()
	fake.Engine = nativeEngine
	fake.Gateway = testGateway
	stubInterfaces(t, hostIPs)
}

func TestClassifyEngine(t *testing.T) {
	cases := []struct {
		name string
		info docker.EngineInfo
		gw   string
		ips  []net.IP
		want EngineKind
	}{
		{"native linux", nativeEngine, testGateway, hostIPs, EngineNativeLinux},
		{"docker desktop mac", docker.DesktopEngine, testGateway, nil, EngineLoopbackForwarded},
		// Desktop on Linux still runs the engine in a VM: no relay.
		{"docker desktop linux", docker.EngineInfo{OperatingSystem: "Docker Desktop", OSType: "linux", Name: "docker-desktop"}, testGateway, hostIPs, EngineLoopbackForwarded},
		{"desktop by label only", docker.EngineInfo{OSType: "linux", Labels: []string{"com.docker.desktop.address=x"}}, testGateway, hostIPs, EngineLoopbackForwarded},
		{"orbstack", docker.EngineInfo{OperatingSystem: "OrbStack", OSType: "linux", Name: "orbstack"}, testGateway, nil, EngineLoopbackForwarded},
		{"colima reports plain ubuntu", docker.EngineInfo{OperatingSystem: "Ubuntu 24.04.1 LTS", OSType: "linux", Name: "colima"}, testGateway, nil, EngineLoopbackForwarded},
		{"colima profile", docker.EngineInfo{OperatingSystem: "Ubuntu", OSType: "linux", Name: "colima-work"}, testGateway, nil, EngineLoopbackForwarded},
		{"rancher desktop", docker.EngineInfo{OperatingSystem: "Rancher Desktop WSL Distribution", OSType: "linux", Name: "x"}, testGateway, nil, EngineLoopbackForwarded},
		{"lima", docker.EngineInfo{OperatingSystem: "Alpine Linux", OSType: "linux", Name: "lima-default"}, testGateway, nil, EngineLoopbackForwarded},
		{"unmarked VM: gateway not local", docker.EngineInfo{OperatingSystem: "Ubuntu", OSType: "linux", Name: "box"}, testGateway, []net.IP{net.ParseIP("127.0.0.1")}, EngineUnknown},
		{"windows engine", docker.EngineInfo{OperatingSystem: "Windows Server", OSType: "windows"}, testGateway, hostIPs, EngineUnknown},
		{"empty info", docker.EngineInfo{}, testGateway, hostIPs, EngineUnknown},
		{"ipv6 gateway", nativeEngine, "fd00::1", []net.IP{net.ParseIP("fd00::1")}, EngineUnknown},
		{"garbage gateway", nativeEngine, "nope", hostIPs, EngineUnknown},
		{"loopback gateway", nativeEngine, "127.0.0.1", hostIPs, EngineUnknown},
		{"unspecified gateway", nativeEngine, "0.0.0.0", []net.IP{net.IPv4zero}, EngineUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hn := classifyEngine(tc.info, tc.gw, tc.ips)
			if hn.Kind != tc.want {
				t.Fatalf("kind = %v (%s), want %v", hn.Kind, hn.Reason, tc.want)
			}
			if hn.Reason == "" {
				t.Error("reason should always be set")
			}
			if (hn.Gateway != "") != (tc.want == EngineNativeLinux) {
				t.Errorf("gateway = %q; only native linux should carry one", hn.Gateway)
			}
		})
	}
}

func TestEngineKindString(t *testing.T) {
	for k, want := range map[EngineKind]string{
		EngineUnknown: "unknown", EngineLoopbackForwarded: "desktop-or-vm", EngineNativeLinux: "native-linux",
	} {
		if k.String() != want {
			t.Errorf("%d.String() = %q, want %q", k, k.String(), want)
		}
	}
}

func TestHostNetworkFallsBackToUnknown(t *testing.T) {
	cases := map[string]func(t *testing.T, fd *docker.Fake){
		"info error": func(_ *testing.T, fd *docker.Fake) { fd.EngineInfoErr = errors.New("boom") },
		"gateway error": func(_ *testing.T, fd *docker.Fake) {
			fd.Engine = nativeEngine
			fd.GatewayErr = errors.New("no bridge")
		},
		"iface error": func(t *testing.T, fd *docker.Fake) {
			fd.Engine = nativeEngine
			orig := interfaceAddrs
			interfaceAddrs = func() ([]net.IP, error) { return nil, errors.New("no ifaces") }
			t.Cleanup(func() { interfaceAddrs = orig })
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			fd := docker.NewFake()
			setup(t, fd)
			a := NewWithPaths(Paths{}, nil, fd)
			if hn := a.hostNetwork(); hn.Kind != EngineUnknown || hn.Reason == "" {
				t.Errorf("hostNetwork = %+v", hn)
			}
		})
	}
}

func TestHostNetworkDesktopSkipsGatewayLookup(t *testing.T) {
	fd := docker.NewFake()
	fd.GatewayErr = errors.New("must not be needed")
	a := NewWithPaths(Paths{}, nil, fd)
	if hn := a.hostNetwork(); hn.Kind != EngineLoopbackForwarded {
		t.Errorf("hostNetwork = %+v", hn)
	}
}

func TestHostNetworkIsCached(t *testing.T) {
	fd := docker.NewFake()
	useNativeLinux(t, fd)
	a := NewWithPaths(Paths{}, nil, fd)
	if a.hostNetwork().Kind != EngineNativeLinux {
		t.Fatal("expected native linux")
	}
	fd.EngineInfoErr = errors.New("would now fail")
	if a.hostNetwork().Kind != EngineNativeLinux {
		t.Error("classification should be computed once per App")
	}
}

// desktopRunArgs is the exact `docker run` argv caddy_shack generates for
// the Caddy container on Docker Desktop (macOS). It is the pre-native-Linux
// argv with one deliberate change: the boot script resumes Caddy's
// autosave, so restarts outside caddy_shack no longer drop every route.
var desktopRunArgs = []string{
	"run", "-d", "--name", "caddy_shack", "--restart", "unless-stopped",
	"-p", "127.0.0.1:80:80", "-p", "127.0.0.1:443:443", "-p", "127.0.0.1:2019:2019",
	"-v", "caddy_shack_data:/data", "-v", "caddy_shack_config:/config",
	"caddy:2",
	"sh", "-c", wantBootScript,
}

// wantBootScript is the Caddy container command, pinned literally so a
// change to how Caddy boots (and so whether it resumes its autosave after
// an out-of-band restart) is always deliberate.
const wantBootScript = "printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
	"if caddy validate --config /config/caddy/autosave.json >/dev/null 2>&1; then " +
	"exec caddy run --resume --config /etc/caddy/Caddyfile --adapter caddyfile; fi; " +
	"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"

func TestCaddyRunArgsDesktopUnchanged(t *testing.T) {
	for _, info := range []docker.EngineInfo{
		docker.DesktopEngine,
		{OperatingSystem: "OrbStack", OSType: "linux", Name: "orbstack"},
		{OperatingSystem: "Ubuntu", OSType: "linux", Name: "colima"},
		{OperatingSystem: "Ubuntu", OSType: "linux", Name: "box"}, // unknown: keeps today's args
	} {
		hn := classifyEngine(info, testGateway, nil)
		got := docker.RunArgs(caddyContainerSpec(DefaultInstance(), CaddyImage, hn))
		if strings.Join(got, "\x00") != strings.Join(desktopRunArgs, "\x00") {
			t.Errorf("%s (%s):\n got  %q\n want %q", info.Name, hn.Kind, got, desktopRunArgs)
		}
	}
}

func TestCaddyRunArgsNativeLinux(t *testing.T) {
	hn := classifyEngine(nativeEngine, testGateway, hostIPs)
	got := strings.Join(docker.RunArgs(caddyContainerSpec(DefaultInstance(), CaddyImage, hn)), " ")
	want := "run -d --name caddy_shack --restart unless-stopped " +
		"--add-host host.docker.internal:host-gateway " +
		"-p 127.0.0.1:80:80 -p 127.0.0.1:443:443 -p 127.0.0.1:2019:2019 " +
		"-v caddy_shack_data:/data -v caddy_shack_config:/config -v caddy_shack_relay_socket:/run/caddy_shack " +
		"caddy:2 sh -c " + wantBootScript
	if got != want {
		t.Errorf("\n got  %s\n want %s", got, want)
	}
}

func TestRelayVolumeNameDiffersFromContainers(t *testing.T) {
	if RelayVolume == RelayContainerName || RelayVolume == CaddyContainerName {
		t.Errorf("volume %q shares a container's name; bare docker inspect becomes ambiguous", RelayVolume)
	}
}

func TestRelayRunArgs(t *testing.T) {
	args := docker.RunArgs(relayContainerSpec(DefaultInstance(), CaddyImage))
	head := strings.Join(args[:13], " ")
	want := "run -d --name caddy_shack_relay --restart unless-stopped --network host " +
		"-v caddy_shack_relay_socket:/run/caddy_shack caddy:2 sh -c"
	if head != want {
		t.Errorf("\n got  %s\n want %s", head, want)
	}
	if len(args) != 15 || args[14] != relayConfig() {
		t.Errorf("relay config must be passed as $0 of the script: %q", args[13:])
	}
	for _, a := range args {
		if a == "-p" {
			t.Errorf("relay must not publish ports: %q", args)
		}
	}
}

// TestSpecsNeverBindWildcard guards "nothing listens on 0.0.0.0 or a LAN
// interface": every published port on every engine is loopback-only.
func TestSpecsNeverBindWildcard(t *testing.T) {
	for _, hn := range []HostNetwork{
		{Kind: EngineUnknown}, {Kind: EngineLoopbackForwarded}, {Kind: EngineNativeLinux, Gateway: testGateway},
	} {
		for _, spec := range []docker.ContainerSpec{caddyContainerSpec(DefaultInstance(), CaddyImage, hn), relayContainerSpec(DefaultInstance(), CaddyImage)} {
			for _, p := range spec.PortMappings {
				if p.HostAddr != "127.0.0.1" {
					t.Errorf("%s/%s: binds %s:%d", hn.Kind, spec.Name, p.HostAddr, p.HostPort)
				}
			}
		}
	}
}

func TestRelayConfig(t *testing.T) {
	var cfg struct {
		Admin struct {
			Disabled bool `json:"disabled"`
		} `json:"admin"`
		Apps struct {
			HTTP struct {
				Servers map[string]struct {
					Listen             []string `json:"listen"`
					TrustedProxiesUnix bool     `json:"trusted_proxies_unix"`
					Routes             []struct {
						Handle []struct {
							Upstreams []struct {
								Dial string `json:"dial"`
							} `json:"upstreams"`
							Headers struct {
								Request struct {
									Delete []string `json:"delete"`
								} `json:"request"`
							} `json:"headers"`
						} `json:"handle"`
					} `json:"routes"`
				} `json:"servers"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(relayConfig()), &cfg); err != nil {
		t.Fatalf("relay config is not JSON: %v", err)
	}
	if !cfg.Admin.Disabled {
		t.Error("relay admin must be disabled (it would collide with caddy_shack's 127.0.0.1:2019)")
	}
	srv, ok := cfg.Apps.HTTP.Servers["relay"]
	if !ok || len(cfg.Apps.HTTP.Servers) != 1 {
		t.Fatalf("want exactly one 'relay' server: %+v", cfg.Apps.HTTP.Servers)
	}
	if strings.Join(srv.Listen, ",") != "unix//run/caddy_shack/relay.sock" {
		t.Errorf("relay must listen only on its unix socket: %v", srv.Listen)
	}
	if !srv.TrustedProxiesUnix {
		t.Error("trusted_proxies_unix is required or X-Forwarded-* from caddy_shack is dropped")
	}
	h := srv.Routes[0].Handle[0]
	if h.Upstreams[0].Dial != "localhost:{http.request.header.X-Caddy-Shack-Port}" {
		t.Errorf("dial: %q", h.Upstreams[0].Dial)
	}
	if strings.Join(h.Headers.Request.Delete, ",") != RelayPortHeader {
		t.Errorf("relay should strip %s before the backend: %v", RelayPortHeader, h.Headers.Request.Delete)
	}
}

func TestSpecDrift(t *testing.T) {
	native := caddyContainerSpec(DefaultInstance(), CaddyImage, HostNetwork{Kind: EngineNativeLinux, Gateway: testGateway})
	legacy := docker.ContainerInfo{
		NetworkMode: "bridge",
		Mounts: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		Command: native.Command,
		Ports:   native.PortMappings,
	}
	if d := specDrift(native, legacy); len(d) != 2 {
		t.Errorf("legacy container should lack add-host and relay volume, got %v", d)
	}
	current := legacy
	current.ExtraHosts = []string{HostGatewayEntry}
	current.Mounts = append(current.Mounts, docker.VolumeMount{VolumeName: RelayVolume, ContainerPath: RelaySocketDir})
	if d := specDrift(native, current); len(d) != 0 {
		t.Errorf("current container should not drift: %v", d)
	}
	current.NetworkMode = "default"
	if d := specDrift(native, current); len(d) != 0 {
		t.Errorf("'default' network mode is the bridge: %v", d)
	}
	relay := relayContainerSpec(DefaultInstance(), CaddyImage)
	if d := specDrift(relay, docker.ContainerInfo{NetworkMode: "bridge", Command: relay.Command}); len(d) != 2 {
		t.Errorf("bridge relay without volume should drift twice: %v", d)
	}
}

// TestSpecDriftFlagsPreResumeCommand: a Caddy container created before the
// boot script resumed its autosave drops every route on any restart
// caddy_shack doesn't drive, so it must be reported as drifted (and so be
// recreated by install) on every engine.
func TestSpecDriftFlagsPreResumeCommand(t *testing.T) {
	preResume := []string{"sh", "-c", "printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
		"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"}
	for _, hn := range []HostNetwork{{Kind: EngineLoopbackForwarded}, {Kind: EngineNativeLinux, Gateway: testGateway}} {
		spec := caddyContainerSpec(DefaultInstance(), CaddyImage, hn)
		info := docker.ContainerInfo{NetworkMode: "bridge", ExtraHosts: spec.ExtraHosts, Mounts: spec.Volumes, Command: preResume, Ports: spec.PortMappings}
		d := specDrift(spec, info)
		if len(d) != 1 || d[0] != "outdated container command" {
			t.Errorf("%s: want only the command flagged, got %v", hn.Kind, d)
		}
		info.Command = spec.Command
		if d := specDrift(spec, info); len(d) != 0 {
			t.Errorf("%s: current command should not drift: %v", hn.Kind, d)
		}
	}
}

func TestBuildConfigPerEngine(t *testing.T) {
	s := &state.State{SchemaVersion: 1, Backends: map[string]state.Backend{
		"mail": {Domain: "mail.test", Upstream: "host.docker.internal:8025"},
		"app":  {Domain: "app.test", Upstream: "app:80"},
	}}

	desktop := NewWithPaths(Paths{}, nil, docker.NewFake())
	if string(mustBuildConfig(t, desktop, s)) != string(caddyapi.Build(s)) {
		t.Error("Docker Desktop config must be byte-identical to caddyapi.Build")
	}

	fd := docker.NewFake()
	useNativeLinux(t, fd)
	native := string(mustBuildConfig(t, NewWithPaths(Paths{}, nil, fd), s))
	if !strings.Contains(native, `"dial": "unix//run/caddy_shack/relay.sock"`) ||
		!strings.Contains(native, `"X-Caddy-Shack-Port": [`) {
		t.Errorf("native host-mode route should dial the relay:\n%s", native)
	}
	if strings.Contains(native, "host.docker.internal:8025") {
		t.Error("native config must not dial host.docker.internal")
	}
	if !strings.Contains(native, `"dial": "app:80"`) {
		t.Error("container-mode route must be unchanged on native linux")
	}
}

func TestSpecDriftPorts(t *testing.T) {
	spec := docker.ContainerSpec{PortMappings: []docker.PortMapping{{HostAddr: "192.168.1.12", HostPort: 443, ContainerPort: 8443}}}
	moved := docker.ContainerInfo{NetworkMode: "bridge", Ports: []docker.PortMapping{{HostAddr: "192.168.1.40", HostPort: 443, ContainerPort: 8443}}}
	d := specDrift(spec, moved)
	if len(d) != 1 || !strings.Contains(d[0], "port bindings") {
		t.Errorf("moved address should report port drift, got %v", d)
	}
	same := docker.ContainerInfo{NetworkMode: "bridge", Ports: spec.PortMappings}
	if d := specDrift(spec, same); len(d) != 0 {
		t.Errorf("identical bindings should not drift: %v", d)
	}
	if d := specDrift(docker.ContainerSpec{}, moved); len(d) != 0 {
		t.Errorf("a spec without ports never reports port drift: %v", d)
	}
}

func TestCaddySpecHasNoPortDriftAgainstItself(t *testing.T) {
	spec := caddyContainerSpec(DefaultInstance(), CaddyImage, HostNetwork{})
	fake := docker.NewFake()
	if err := fake.CreateAndStart(spec); err != nil {
		t.Fatal(err)
	}
	info, err := fake.InspectContainer(spec.Name)
	if err != nil {
		t.Fatal(err)
	}
	if d := specDrift(spec, info); len(d) != 0 {
		t.Errorf("caddy spec should not drift against itself: %v", d)
	}
}
