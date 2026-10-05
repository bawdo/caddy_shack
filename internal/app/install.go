package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// caddyContainerSpec returns the ContainerSpec for the managed Caddy
// container running the given image on the given engine. Shared by install
// and upgrade so the two cannot drift. Docker Desktop, VM runtimes, and
// unclassified engines get exactly the original spec; native Linux adds the
// host.docker.internal entry Desktop provides for free and the relay socket
// volume host-mode backends are routed through.
func caddyContainerSpec(image string, hn HostNetwork) docker.ContainerSpec {
	spec := docker.ContainerSpec{
		Name:  CaddyContainerName,
		Image: image,
		PortMappings: []docker.PortMapping{
			{HostAddr: "127.0.0.1", HostPort: 80, ContainerPort: 80},
			{HostAddr: "127.0.0.1", HostPort: 443, ContainerPort: 443},
			{HostAddr: "127.0.0.1", HostPort: 2019, ContainerPort: 2019},
		},
		Volumes: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		// Write a minimal Caddyfile that binds the admin endpoint to
		// 0.0.0.0:2019 so the host port-forward can reach it (caddy:2's
		// default binds admin to localhost:2019, invisible from outside the
		// container). First `register` swaps the whole config via POST /load,
		// so the Caddyfile contents don't matter beyond bootstrapping the
		// admin listener.
		Command: []string{
			"sh", "-c",
			"printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
				"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile",
		},
	}
	if hn.Kind == EngineNativeLinux {
		spec.ExtraHosts = []string{HostGatewayEntry}
		spec.Volumes = append(spec.Volumes, docker.VolumeMount{VolumeName: RelayVolume, ContainerPath: RelaySocketDir})
	}
	return spec
}

// Install ensures Caddy is installed and running. Idempotent: a no-op if
// the container is already running with the settings this engine needs;
// starts it if stopped; creates from scratch if absent. An existing
// container created without this engine's settings (e.g. before native
// Linux support) is recreated, keeping its volumes, networks, and backends.
func (a *App) Install(ctx context.Context) error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	hn := a.hostNetwork()
	a.noteUnknownEngine(hn)

	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	if state != docker.StateAbsent {
		drift, err := a.caddyDrift()
		if err != nil {
			return err
		}
		if len(drift) > 0 {
			a.notef("recreating %s for the %s engine (%s); volumes, the local CA, networks and registered backends are kept\n",
				CaddyContainerName, hn.Kind, strings.Join(drift, "; "))
			return a.recreateCaddy(ctx, CaddyImage)
		}
	}

	switch state {
	case docker.StateRunning:
		return a.ensureRelay()
	case docker.StateStopped:
		if err := a.docker.Start(CaddyContainerName); err != nil {
			return err
		}
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("attach shared network: %w", err)
		}
		return a.ensureRelay()
	case docker.StateAbsent:
		// fall through
	}

	if err := a.ensureCaddyVolumes(); err != nil {
		return err
	}
	if err := a.docker.PullImage(CaddyImage); err != nil {
		return err
	}
	spec := caddyContainerSpec(CaddyImage, hn)
	if err := a.docker.CreateAndStart(spec); err != nil {
		// Roll back the half-created container so a retry has a clean slate.
		// Ignoring the Remove error: the user already has a more useful error
		// from CreateAndStart, and there's nothing actionable for them in a
		// secondary rm failure.
		_ = a.docker.Remove(CaddyContainerName)
		if isPortAllocatedErr(err) {
			return fmt.Errorf("install: one of ports 80, 443, or 2019 on 127.0.0.1 is already in use. "+
				"Check other containers with 'docker ps' or processes with 'sudo lsof -nP -iTCP:80,443,2019 -sTCP:LISTEN'. "+
				"Underlying error: %w", err)
		}
		return err
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return fmt.Errorf("attach shared network: %w", err)
	}
	return a.ensureRelay()
}

// isPortAllocatedErr returns true when the docker error indicates a host
// port is already bound. Matches the exact wording in docker daemon's
// "Bind for <addr>:<port> failed: port is already allocated" message.
func isPortAllocatedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "port is already allocated")
}
