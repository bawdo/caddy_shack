package app

import (
	"fmt"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// Start starts the existing CaddyShack container.
func (a *App) Start() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	switch state {
	case docker.StateAbsent:
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	case docker.StateRunning:
		return nil
	case docker.StateStopped:
		return a.docker.Start(CaddyContainerName)
	}
	return nil
}

// Stop stops the CaddyShack container if running. No-op if absent or stopped.
func (a *App) Stop() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if state == docker.StateRunning {
		return a.docker.Stop(CaddyContainerName)
	}
	return nil
}

// Restart stops then starts. Tolerates absent (returns ErrNotInstalled).
func (a *App) Restart() error {
	if err := a.Stop(); err != nil {
		return err
	}
	return a.Start()
}
