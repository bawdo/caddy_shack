package app

import (
	"fmt"
	"strings"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// Install ensures Caddy is installed and running. Idempotent: a no-op if
// the container is already running; starts it if stopped; creates from
// scratch if absent.
func (a *App) Install() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}

	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	switch state {
	case docker.StateRunning:
		return nil
	case docker.StateStopped:
		return a.docker.Start(CaddyContainerName)
	case docker.StateAbsent:
		// fall through
	}

	if err := a.docker.EnsureVolume(CaddyDataVolume); err != nil {
		return err
	}
	if err := a.docker.EnsureVolume(CaddyConfigVolume); err != nil {
		return err
	}
	if err := a.docker.PullImage(CaddyImage); err != nil {
		return err
	}
	spec := docker.ContainerSpec{
		Name:  CaddyContainerName,
		Image: CaddyImage,
		PortMappings: []docker.PortMapping{
			{HostAddr: "127.0.0.1", HostPort: 80, ContainerPort: 80},
			{HostAddr: "127.0.0.1", HostPort: 443, ContainerPort: 443},
			{HostAddr: "127.0.0.1", HostPort: 2019, ContainerPort: 2019},
		},
		Volumes: []docker.VolumeMount{
			{VolumeName: CaddyDataVolume, ContainerPath: "/data"},
			{VolumeName: CaddyConfigVolume, ContainerPath: "/config"},
		},
		// Start with an empty admin-enabled config; first register populates it.
		Command: []string{"caddy", "run", "--config", "/etc/caddy/Caddyfile", "--adapter", "caddyfile"},
	}
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
	return nil
}

// isPortAllocatedErr returns true when the docker error indicates a host
// port is already bound. Matches the exact wording in docker daemon's
// "Bind for <addr>:<port> failed: port is already allocated" message.
func isPortAllocatedErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "port is already allocated")
}
