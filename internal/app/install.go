package app

import (
	"errors"
	"fmt"

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
		var rejErr interface{}
		if errors.As(err, &rejErr) {
			// Surface as generic so cmd layer prints it and exits 1.
		}
		return err
	}
	return nil
}
