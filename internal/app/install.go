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
func caddyContainerSpec(inst Instance, image string, hn HostNetwork) docker.ContainerSpec {
	spec := docker.ContainerSpec{
		Name:  inst.ContainerName,
		Image: image,
		PortMappings: []docker.PortMapping{
			{HostAddr: "127.0.0.1", HostPort: inst.HTTPPort, ContainerPort: 80},
			{HostAddr: "127.0.0.1", HostPort: inst.HTTPSPort, ContainerPort: 443},
			{HostAddr: "127.0.0.1", HostPort: inst.AdminPort, ContainerPort: 2019},
		},
		Volumes: []docker.VolumeMount{
			{VolumeName: inst.DataVolume, ContainerPath: "/data"},
			{VolumeName: inst.ConfigVolume, ContainerPath: "/config"},
		},
		Command: []string{"sh", "-c", caddyBootScript},
	}
	if hn.Kind == EngineNativeLinux {
		spec.ExtraHosts = []string{HostGatewayEntry}
		spec.Volumes = append(spec.Volumes, docker.VolumeMount{VolumeName: inst.RelayVolume, ContainerPath: RelaySocketDir})
	}
	return spec
}

// caddyAutosavePath is where Caddy autosaves every config it loads. /config
// is the caddy_shack_config volume (the image sets XDG_CONFIG_HOME=/config),
// so the file survives container restarts and recreates.
const caddyAutosavePath = "/config/caddy/autosave.json"

// caddyBootScript is the Caddy container's command. It writes a minimal
// Caddyfile that binds the admin endpoint to 0.0.0.0:2019 so the host
// port-forward can reach it (caddy:2's default binds admin to
// localhost:2019, invisible from outside the container).
//
// Caddy is started with --resume so any restart CaddyShack doesn't drive
// (`docker restart`, a docker daemon restart, a host reboot under the
// unless-stopped policy) comes back serving the last config CaddyShack
// pushed rather than the bare admin-only Caddyfile. Without it Caddy
// boots with no http or tls app and, worse, overwrites the autosave with
// that empty config. With no autosave yet, --resume falls back to the
// Caddyfile. An autosave Caddy can't load would make `caddy run --resume`
// exit, and the restart policy would crash-loop it with the admin API
// down, so it is validated first and skipped if invalid; CaddyShack's next
// reconcile then re-pushes the registry.
const caddyBootScript = "printf '{\\n\\tadmin 0.0.0.0:2019\\n}\\n' > /etc/caddy/Caddyfile && " +
	"if caddy validate --config " + caddyAutosavePath + " >/dev/null 2>&1; then " +
	"exec caddy run --resume --config /etc/caddy/Caddyfile --adapter caddyfile; fi; " +
	"exec caddy run --config /etc/caddy/Caddyfile --adapter caddyfile"

// Install ensures Caddy is installed, running, and serving the registry.
// Idempotent: when the container is already running with the settings this
// engine needs it only reconciles Caddy with the registry; starts it if
// stopped; creates from scratch if absent. An existing
// container created without this engine's settings (e.g. before native
// Linux support) is recreated, keeping its volumes, networks, and backends.
func (a *App) Install(ctx context.Context) error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	hn := a.hostNetwork()
	a.noteUnknownEngine(hn)

	state, err := a.docker.ContainerState(a.inst.ContainerName)
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
				a.inst.ContainerName, hn.Kind, strings.Join(drift, "; "))
			if err := a.recreateCaddy(ctx, CaddyImage); err != nil {
				return err
			}
			return a.ensureLANFromState()
		}
	}

	switch state {
	case docker.StateRunning, docker.StateRestarting:
		// A restarting container is left to docker; waitForAdmin bounds the wait.
		return a.restoreThenRelay(ctx)
	case docker.StateStopped:
		if err := a.docker.Start(a.inst.ContainerName); err != nil {
			return err
		}
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("attach shared network: %w", err)
		}
		return a.restoreThenRelay(ctx)
	case docker.StateAbsent:
		// fall through
	}

	if err := a.ensureCaddyVolumes(); err != nil {
		return err
	}
	if err := a.docker.PullImage(CaddyImage); err != nil {
		return err
	}
	spec := caddyContainerSpec(a.inst, CaddyImage, hn)
	if err := a.docker.CreateAndStart(spec); err != nil {
		// Roll back the half-created container so a retry has a clean slate.
		// Ignoring the Remove error: the user already has a more useful error
		// from CreateAndStart, and there's nothing actionable for them in a
		// secondary rm failure.
		_ = a.docker.Remove(a.inst.ContainerName)
		if addrInUse.MatchString(err.Error()) {
			ports := a.inst.publishedPorts()
			return fmt.Errorf("install: one of ports %d, %d, or %d on 127.0.0.1 is already in use. "+
				"Check other containers with 'docker ps' or processes with 'sudo lsof -nP -iTCP:%d,%d,%d -sTCP:LISTEN'. "+
				"Underlying error: %w", ports[0], ports[1], ports[2], ports[0], ports[1], ports[2], err)
		}
		return err
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return fmt.Errorf("attach shared network: %w", err)
	}
	// The registry may already hold backends (e.g. the container was
	// removed with `docker rm` and is being reinstalled), so a fresh
	// container is reconciled too.
	return a.restoreThenRelay(ctx)
}
