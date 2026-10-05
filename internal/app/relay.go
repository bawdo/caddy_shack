package app

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// On native Linux, host.docker.internal resolves to the docker0 gateway,
// which cannot reach services bound to the host's 127.0.0.1 and is usually
// firewalled (ufw drops container->host traffic). Host-mode backends are
// instead reached through a relay: a second Caddy in the host's network
// namespace whose only listener is a unix socket in a volume shared with
// the main container. The main Caddy dials that socket and names the
// backend's port in RelayPortHeader; the relay forwards to localhost:<port>.
// No TCP listener is added and no firewall rule is needed. Docker Desktop
// never gets a relay.
const (
	// RelayContainerName is the host-network relay container (native Linux
	// only).
	RelayContainerName = "caddy_shack_relay"
	// RelayVolume holds the relay's unix socket; mounted into both
	// containers at RelaySocketDir. Deliberately not named like the relay
	// container, so a bare `docker inspect caddy_shack_relay` is never
	// ambiguous.
	RelayVolume = "caddy_shack_relay_socket"
	// RelaySocketDir is where RelayVolume is mounted in both containers.
	RelaySocketDir = "/run/caddy_shack"
	// RelaySocketPath is the relay's listening socket.
	RelaySocketPath = RelaySocketDir + "/relay.sock"
	// RelayPortHeader carries a host-mode backend's port from the main Caddy
	// to the relay.
	RelayPortHeader = "X-Caddy-Shack-Port"
	// HostGatewayEntry is the --add-host that gives native Linux containers
	// the host.docker.internal name Docker Desktop provides automatically.
	HostGatewayEntry = UpstreamHost + ":host-gateway"
)

// relayConfig is the relay's static Caddy config. The admin API is disabled
// so it can't collide with caddy_shack's published 127.0.0.1:2019 in the
// host namespace. trusted_proxies_unix makes the relay pass the main
// Caddy's X-Forwarded-For/Proto/Host through untouched, so backends still
// see https and the browser's address rather than the relay's view. The
// upstream is localhost, not 127.0.0.1: Go's dialer tries every address
// localhost resolves to, so services bound only to ::1 (e.g. a dev server
// told to listen on "localhost") are reached as well as 127.0.0.1 ones.
func relayConfig() string {
	cfg := map[string]any{
		"admin": map[string]any{"disabled": true},
		"apps": map[string]any{
			"http": map[string]any{
				"servers": map[string]any{
					"relay": map[string]any{
						"listen":               []string{"unix/" + RelaySocketPath},
						"automatic_https":      map[string]any{"disable": true},
						"trusted_proxies_unix": true,
						"routes": []any{map[string]any{
							"handle": []any{map[string]any{
								"handler": "reverse_proxy",
								"headers": map[string]any{
									"request": map[string]any{"delete": []string{RelayPortHeader}},
								},
								"upstreams": []any{map[string]any{
									"dial": "localhost:{http.request.header." + RelayPortHeader + "}",
								}},
							}},
						}},
					},
				},
			},
		},
	}
	out, _ := json.Marshal(cfg)
	return string(out)
}

// relayContainerSpec is the relay container. The config travels as $0 of
// the sh -c script so no shell quoting is involved; a stale socket from a
// previous run is removed before Caddy binds it.
func relayContainerSpec(inst Instance, image string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:        inst.RelayContainerName,
		Image:       image,
		NetworkMode: "host",
		Volumes: []docker.VolumeMount{
			{VolumeName: inst.RelayVolume, ContainerPath: RelaySocketDir},
		},
		Command: []string{
			"sh", "-c",
			"rm -f " + RelaySocketPath + " && printf '%s' \"$0\" > /etc/caddy/relay.json && " +
				"exec caddy run --config /etc/caddy/relay.json",
			relayConfig(),
		},
	}
}

// caddyOptions picks how host-mode upstreams are rendered for this engine.
func (a *App) caddyOptions() caddyapi.Options {
	if a.hostNetwork().Kind != EngineNativeLinux {
		return caddyapi.Options{}
	}
	return caddyapi.Options{HostRelay: &caddyapi.HostRelay{
		Host:       UpstreamHost,
		Dial:       "unix/" + RelaySocketPath,
		PortHeader: RelayPortHeader,
	}}
}

// buildConfig renders the Caddy config for s on this engine. Every push to
// Caddy and the drift check go through here so they cannot disagree.
func (a *App) buildConfig(s *state.State) []byte {
	return caddyapi.BuildWith(s, a.caddyOptions())
}

// ensureRelay makes the relay container exist, match its spec, and run, on
// native Linux. A no-op on every other engine. The relay holds no state, so
// a mismatched one is simply replaced.
func (a *App) ensureRelay() error {
	if a.hostNetwork().Kind != EngineNativeLinux {
		return nil
	}
	spec := relayContainerSpec(a.inst, CaddyImage)
	st, err := a.docker.ContainerState(a.inst.RelayContainerName)
	if err != nil {
		return fmt.Errorf("inspect relay: %w", err)
	}
	if st != docker.StateAbsent {
		info, err := a.docker.InspectContainer(a.inst.RelayContainerName)
		if err != nil {
			return fmt.Errorf("inspect relay: %w", err)
		}
		if len(specDrift(spec, info)) == 0 {
			if st == docker.StateStopped {
				return a.docker.Start(a.inst.RelayContainerName)
			}
			return nil
		}
		if err := a.docker.Remove(a.inst.RelayContainerName); err != nil {
			return err
		}
	}
	if err := a.docker.EnsureVolume(a.inst.RelayVolume); err != nil {
		return err
	}
	if err := a.docker.CreateAndStart(spec); err != nil {
		_ = a.docker.Remove(a.inst.RelayContainerName)
		return fmt.Errorf("start relay: %w", err)
	}
	return nil
}

// stopRelay stops the relay if it is running. Safe on any engine: the relay
// only ever exists on native Linux.
func (a *App) stopRelay() error {
	st, err := a.docker.ContainerState(a.inst.RelayContainerName)
	if err != nil {
		return fmt.Errorf("inspect relay: %w", err)
	}
	if st == docker.StateRunning {
		return a.docker.Stop(a.inst.RelayContainerName)
	}
	return nil
}

// specDrift lists the creation-time settings spec asks for that an existing
// container (info) lacks. Only settings `docker run` fixes for a
// container's lifetime that CaddyShack has changed between releases are
// compared: network mode, extra hosts, volume mounts, and the command
// (e.g. a Caddy container that predates --resume), and published port
// bindings when the spec has any. Empty means up to date.
func specDrift(spec docker.ContainerSpec, info docker.ContainerInfo) []string {
	var drift []string
	want := spec.NetworkMode
	if want == "" {
		want = "bridge"
	}
	got := info.NetworkMode
	if got == "" || got == "default" {
		got = "bridge"
	}
	if got != want {
		drift = append(drift, fmt.Sprintf("network mode is %s, want %s", got, want))
	}
	for _, h := range spec.ExtraHosts {
		if !slices.Contains(info.ExtraHosts, h) {
			drift = append(drift, "missing --add-host "+h)
		}
	}
	for _, v := range spec.Volumes {
		if !slices.Contains(info.Mounts, v) {
			drift = append(drift, fmt.Sprintf("missing volume %s:%s", v.VolumeName, v.ContainerPath))
		}
	}
	if len(spec.Command) > 0 && !slices.Equal(spec.Command, info.Command) {
		drift = append(drift, "outdated container command")
	}
	if len(spec.PortMappings) > 0 {
		want := slices.Clone(spec.PortMappings)
		docker.SortPortMappings(want)
		got := slices.Clone(info.Ports)
		docker.SortPortMappings(got)
		if !slices.Equal(got, want) {
			drift = append(drift, fmt.Sprintf("port bindings differ: got %v, want %v", got, want))
		}
	}
	return drift
}
