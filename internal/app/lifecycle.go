package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// Start starts the existing CaddyShack container, or confirms it is running,
// and makes Caddy's live config match the registry. Caddy resumes its
// autosave on boot, but a container created by an older caddy_shack boots
// from the bare admin-only Caddyfile, and any Caddy may have drifted while
// caddy_shack wasn't looking (e.g. a restart outside it before the
// autosave existed). So both paths reconcile: start after a reboot is how
// a user puts things right, and it must not be a no-op.
//
// Routes are restored before the relay is ensured, and a failure of one
// doesn't skip the other: a relay problem then leaves only host-mode
// backends down (and is reported) instead of a Caddy with no routes at all.
func (a *App) Start(ctx context.Context) error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	switch st {
	case docker.StateAbsent:
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	case docker.StateStopped:
		if err := a.docker.Start(a.inst.ContainerName); err != nil {
			return err
		}
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("attach shared network: %w", err)
		}
	}
	a.noteStaleCaddy()
	return a.restoreThenRelay(ctx)
}

// restoreThenRelay restores the registry into Caddy, then ensures the relay,
// attempting both and reporting every failure.
func (a *App) restoreThenRelay(ctx context.Context) error {
	restoreErr := a.restoreBackends(ctx)
	return errors.Join(restoreErr, a.ensureRelay())
}

// Stop stops the CaddyShack container, and the native-Linux relay, if
// running. No-op if absent or stopped.
func (a *App) Stop() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateRunning {
		if err := a.docker.Stop(a.inst.ContainerName); err != nil {
			return err
		}
	}
	return a.stopRelay()
}

// Restart stops then starts. Tolerates absent (returns ErrNotInstalled).
func (a *App) Restart(ctx context.Context) error {
	if err := a.Stop(); err != nil {
		return err
	}
	return a.Start(ctx)
}
