package app

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

const (
	// LANContainerName is the haproxy forwarder that holds the LAN IP port
	// bindings.
	LANContainerName = "caddy_shack_lan"
	// LANImage is the pinned forwarder image.
	LANImage = "haproxy:3.2-alpine"
)

// LANResolver finds and checks the machine's LAN address. The App depends on
// this rather than lanip.Source so tests can stub it directly.
type LANResolver interface {
	Discover() (lanip.Result, error)
	Present(ip netip.Addr) (bool, error)
}

type systemLANResolver struct{}

func (systemLANResolver) Discover() (lanip.Result, error) { return lanip.Discover(lanip.System()) }
func (systemLANResolver) Present(ip netip.Addr) (bool, error) {
	return lanip.Present(lanip.System(), ip)
}

// WithLANResolver replaces the LAN resolver (for tests) and returns a.
func (a *App) WithLANResolver(r LANResolver) *App {
	a.lan = r
	return a
}

// haproxyConfig forwards raw TCP on 8443 and 8080 to Caddy's LAN listeners,
// prefixing each connection with PROXY protocol v2 so Caddy sees the real
// client address. init-addr none lets haproxy start before Caddy exists.
func haproxyConfig(caddyHost string) string {
	return strings.ReplaceAll(`global
  log stdout format raw local0 warning
defaults
  mode tcp
  timeout connect 5s
  timeout client 1h
  timeout server 1h
resolvers docker
  nameserver dns 127.0.0.11:53
  hold valid 10s
frontend https
  bind :8443
  default_backend caddy_https
frontend http
  bind :8080
  default_backend caddy_http
backend caddy_https
  server caddy <caddy>:8443 send-proxy-v2 resolvers docker init-addr none
backend caddy_http
  server caddy <caddy>:8080 send-proxy-v2 resolvers docker init-addr none
`, "<caddy>", caddyHost)
}

// lanContainerSpec is the forwarder. The config travels as $0 of the sh -c
// script so no shell quoting is involved.
func lanContainerSpec(inst Instance, ip string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:        inst.LANContainerName,
		Image:       LANImage,
		NetworkMode: inst.NetworkName,
		PortMappings: []docker.PortMapping{
			{HostAddr: ip, HostPort: inst.LANHTTPSPort, ContainerPort: caddyapi.LANHTTPSListenPort},
			{HostAddr: ip, HostPort: inst.LANHTTPPort, ContainerPort: caddyapi.LANHTTPListenPort},
		},
		Command: []string{
			"sh", "-c",
			"printf '%s' \"$0\" > /tmp/haproxy.cfg && exec haproxy -W -db -f /tmp/haproxy.cfg",
			haproxyConfig(inst.ContainerName),
		},
	}
}

// ensureLAN makes the forwarder match the LAN settings in s. Disabled means
// no forwarder. If the LAN IP is not on any interface Docker is not touched
// and a wrapped ErrLANDown is returned. The forwarder is stateless, so a
// mismatched one is replaced.
func (a *App) ensureLAN(s *state.State) error {
	if !s.LANEnabled() {
		return a.removeLAN()
	}
	ip, err := netip.ParseAddr(s.LAN.IP)
	if err != nil {
		return fmt.Errorf("LAN IP %q in state is invalid: %w", s.LAN.IP, err)
	}
	ok, err := a.lan.Present(ip)
	if err != nil {
		return fmt.Errorf("check LAN IP %s: %w", ip, err)
	}
	if !ok {
		return fmt.Errorf("%s: %w", ip, ErrLANDown)
	}
	name := a.inst.LANContainerName
	if err := a.docker.EnsureNetwork(a.inst.NetworkName); err != nil {
		return err
	}
	spec := lanContainerSpec(a.inst, s.LAN.IP)
	st, err := a.docker.ContainerState(name)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st != docker.StateAbsent {
		info, err := a.docker.InspectContainer(name)
		if err != nil {
			return fmt.Errorf("inspect LAN forwarder: %w", err)
		}
		if len(specDrift(spec, info)) == 0 {
			if st == docker.StateStopped {
				return a.docker.Start(name)
			}
			return nil
		}
		if err := a.docker.Remove(name); err != nil {
			return err
		}
	}
	if err := a.docker.CreateAndStart(spec); err != nil {
		_ = a.docker.Remove(name)
		return a.lanStartError(s.LAN.IP, err)
	}
	return nil
}

var addrInUse = regexp.MustCompile(`(?i)address already in use|port is already allocated`)

// lanStartError turns Docker's bind failure into a message naming the
// address and port.
func (a *App) lanStartError(ip string, err error) error {
	if addrInUse.MatchString(err.Error()) {
		port := a.inst.LANHTTPSPort
		for _, p := range []int{a.inst.LANHTTPSPort, a.inst.LANHTTPPort} {
			if strings.Contains(err.Error(), fmt.Sprintf("%s:%d", ip, p)) {
				port = p
				break
			}
		}
		return fmt.Errorf("%s:%d is already in use by another process: %w", ip, port, err)
	}
	return fmt.Errorf("start LAN forwarder: %w", err)
}

// stopLAN stops the forwarder if it is running. Safe when absent.
func (a *App) stopLAN() error {
	st, err := a.docker.ContainerState(a.inst.LANContainerName)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st == docker.StateRunning {
		return a.docker.Stop(a.inst.LANContainerName)
	}
	return nil
}

// removeLAN deletes the forwarder if it exists.
func (a *App) removeLAN() error {
	st, err := a.docker.ContainerState(a.inst.LANContainerName)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st == docker.StateAbsent {
		return nil
	}
	return a.docker.Remove(a.inst.LANContainerName)
}

// noteLANDown turns ErrLANDown into a notice (nil) and passes anything else
// through, so start, sync and friends carry on without the forwarder.
func (a *App) noteLANDown(err error) error {
	if errors.Is(err, ErrLANDown) {
		a.notef("notice: LAN exposure is paused: %v\n", err)
		return nil
	}
	return err
}
