package app

import (
	"fmt"
	"net"
	"strings"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// EngineKind classifies the docker engine CaddyShack is talking to. It
// decides how host-mode (--port) backends are reached: Docker Desktop and
// VM runtimes forward host.docker.internal to the host's loopback, native
// Linux Engine does not.
type EngineKind int

const (
	// EngineUnknown could not be classified; CaddyShack keeps the Docker
	// Desktop behaviour (host.docker.internal) and says so.
	EngineUnknown EngineKind = iota
	// EngineLoopbackForwarded is Docker Desktop or a VM-backed runtime
	// (OrbStack, Colima, Lima, Rancher Desktop) that forwards
	// host.docker.internal to ports bound on the host's loopback.
	EngineLoopbackForwarded
	// EngineNativeLinux is Docker Engine sharing this host's kernel and
	// network namespace; host-mode backends go through the relay.
	EngineNativeLinux
)

func (k EngineKind) String() string {
	switch k {
	case EngineLoopbackForwarded:
		return "desktop-or-vm"
	case EngineNativeLinux:
		return "native-linux"
	default:
		return "unknown"
	}
}

// HostNetwork is the classified engine plus, for native Linux, the docker0
// bridge gateway address.
type HostNetwork struct {
	Kind    EngineKind
	Gateway string
	Reason  string
}

// interfaceAddrs is a seam over the host's interface addresses so the
// native-Linux check can be unit-tested off-host.
var interfaceAddrs = func() ([]net.IP, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			ips = append(ips, n.IP)
		}
	}
	return ips, nil
}

// hostNetwork asks the engine what it is and classifies it, once per App.
// It never fails: anything it cannot determine yields EngineUnknown with a
// reason.
func (a *App) hostNetwork() HostNetwork {
	if a.hn == nil {
		hn := a.detectHostNetwork()
		a.hn = &hn
	}
	return *a.hn
}

func (a *App) detectHostNetwork() HostNetwork {
	info, err := a.docker.EngineInfo()
	if err != nil {
		return HostNetwork{Kind: EngineUnknown, Reason: err.Error()}
	}
	if k, why := loopbackForwardedEngine(info); k {
		return HostNetwork{Kind: EngineLoopbackForwarded, Reason: why}
	}
	gw, err := a.docker.BridgeGateway()
	if err != nil {
		return HostNetwork{Kind: EngineUnknown, Reason: err.Error()}
	}
	local, err := interfaceAddrs()
	if err != nil {
		return HostNetwork{Kind: EngineUnknown, Reason: "list host interfaces: " + err.Error()}
	}
	return classifyEngine(info, gw, local)
}

// classifyEngine is the pure classification behind hostNetwork.
//
// Desktop/VM runtimes are recognised by their `docker info` markers. Native
// Linux additionally requires the bridge gateway to be an address on one of
// this host's interfaces: that is what proves the engine shares our network
// namespace, so a host-network container sees the same 127.0.0.1 we do.
// Colima, for example, reports a plain "Ubuntu" OperatingSystem, so the OS
// string alone is not enough.
func classifyEngine(info docker.EngineInfo, gateway string, local []net.IP) HostNetwork {
	if k, why := loopbackForwardedEngine(info); k {
		return HostNetwork{Kind: EngineLoopbackForwarded, Reason: why}
	}
	if info.OSType != "linux" {
		return HostNetwork{Kind: EngineUnknown, Reason: fmt.Sprintf("engine OSType %q is not linux", info.OSType)}
	}
	gw := net.ParseIP(gateway)
	if gw == nil || gw.To4() == nil {
		return HostNetwork{Kind: EngineUnknown, Reason: fmt.Sprintf("bridge gateway %q is not an IPv4 address", gateway)}
	}
	if gw.IsLoopback() || gw.IsUnspecified() {
		return HostNetwork{Kind: EngineUnknown, Reason: fmt.Sprintf("bridge gateway %s is not a bridge address", gateway)}
	}
	for _, ip := range local {
		if ip.Equal(gw) {
			return HostNetwork{
				Kind: EngineNativeLinux, Gateway: gw.String(),
				Reason: fmt.Sprintf("%s engine; bridge gateway %s is on a host interface", info.OperatingSystem, gw),
			}
		}
	}
	return HostNetwork{
		Kind:   EngineUnknown,
		Reason: fmt.Sprintf("bridge gateway %s is not on a host interface (engine in a VM, remote, or rootless?)", gw),
	}
}

// loopbackForwardedEngine reports whether info identifies Docker Desktop or
// a VM-backed runtime known to forward host.docker.internal to the host's
// loopback.
func loopbackForwardedEngine(info docker.EngineInfo) (bool, string) {
	osName := strings.ToLower(info.OperatingSystem)
	name := strings.ToLower(info.Name)
	switch {
	case strings.Contains(osName, "docker desktop"), name == "docker-desktop":
		return true, "Docker Desktop"
	case strings.Contains(osName, "orbstack"), name == "orbstack":
		return true, "OrbStack"
	case strings.Contains(osName, "rancher desktop"), strings.Contains(name, "rancher-desktop"):
		return true, "Rancher Desktop"
	case name == "colima", strings.HasPrefix(name, "colima-"):
		return true, "Colima"
	case strings.HasPrefix(name, "lima-"):
		return true, "Lima"
	}
	for _, l := range info.Labels {
		if strings.HasPrefix(l, "com.docker.desktop.") {
			return true, "Docker Desktop"
		}
	}
	return false, ""
}

// noteUnknownEngine tells the user CaddyShack kept the Docker Desktop
// routing because it could not classify the engine.
func (a *App) noteUnknownEngine(hn HostNetwork) {
	if hn.Kind != EngineUnknown {
		return
	}
	a.notef("could not classify the docker engine (%s); routing host-mode backends via %s as on Docker Desktop, "+
		"which only works if this engine forwards it to the host's loopback\n", hn.Reason, UpstreamHost)
}
