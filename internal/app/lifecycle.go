package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// Start starts the existing CaddyShack container. Caddy always boots from a
// bare admin-only Caddyfile (no --resume), so a container that was actually
// stopped comes back up with no routes or TLS config at all; registered
// backends are re-pushed from state once the admin API is reachable again so
// a stop/start cycle doesn't silently orphan every .test URL.
func (a *App) Start(ctx context.Context) error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	switch st {
	case docker.StateAbsent:
		return fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	case docker.StateRunning:
		return nil
	case docker.StateStopped:
		if err := a.docker.Start(CaddyContainerName); err != nil {
			return err
		}
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("attach shared network: %w", err)
		}
		return a.restoreBackends(ctx)
	}
	return nil
}

// restoreBackends waits for the admin API to come back up, then re-pushes
// every backend from state.json into Caddy. Shared by Start (stopped ->
// running) and Upgrade (recreate); both leave Caddy with nothing but its
// bare admin config until this runs.
func (a *App) restoreBackends(ctx context.Context) error {
	if err := a.waitForAdmin(ctx); err != nil {
		return fmt.Errorf("caddy admin API did not come up; run 'caddy_shack status': %w", err)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	if err := a.caddy.Load(ctx, caddyapi.Build(s)); err != nil {
		return fmt.Errorf("failed to restore backends; run 'caddy_shack status': %w", err)
	}
	return nil
}

// Stop stops the CaddyShack container if running. No-op if absent or stopped.
func (a *App) Stop() error {
	if err := a.docker.DaemonReachable(); err != nil {
		return fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateRunning {
		return a.docker.Stop(CaddyContainerName)
	}
	return nil
}

// Restart stops then starts. Tolerates absent (returns ErrNotInstalled).
func (a *App) Restart(ctx context.Context) error {
	if err := a.Stop(); err != nil {
		return err
	}
	return a.Start(ctx)
}
