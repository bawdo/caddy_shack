package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/state"
)

// ensureCaddyVolumes creates every named volume the Caddy container mounts
// on this engine. Idempotent.
func (a *App) ensureCaddyVolumes() error {
	for _, v := range caddyContainerSpec(CaddyImage, a.hostNetwork()).Volumes {
		if err := a.docker.EnsureVolume(v.VolumeName); err != nil {
			return err
		}
	}
	return nil
}

// caddyDrift lists what the existing Caddy container lacks compared with the
// spec this engine needs (see specDrift). Empty means up to date. Only
// native Linux is ever checked: every other engine's spec is unchanged from
// earlier releases, so there is nothing to migrate and Docker Desktop
// installs are never inspected or recreated by this path.
func (a *App) caddyDrift() ([]string, error) {
	if a.hostNetwork().Kind != EngineNativeLinux {
		return nil, nil
	}
	info, err := a.docker.InspectContainer(CaddyContainerName)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	return specDrift(caddyContainerSpec(CaddyImage, a.hostNetwork()), info), nil
}

// noteStaleCaddy points the user at `install` when the existing container
// predates this engine's settings. Used by commands that must not recreate
// the container themselves (start, register). Best-effort and silent on
// inspection errors.
func (a *App) noteStaleCaddy() {
	drift, err := a.caddyDrift()
	if err != nil || len(drift) == 0 {
		return
	}
	a.notef("%s was created without the settings the %s engine needs (%s); host-mode backends won't be reachable "+
		"until it is recreated. Run: caddy_shack install (keeps volumes, the local CA, networks and backends)\n",
		CaddyContainerName, a.hostNetwork().Kind, strings.Join(drift, "; "))
}

// recreateCaddy replaces the Caddy container with one built from image and
// this engine's spec, then restores everything a fresh `docker run` would
// lose:
//   - the data and config volumes are reused as-is, so the local root CA
//     (and every browser/OS trust of it) and issued certs survive;
//   - every network the old container was attached to is reattached
//     (`docker run` only attaches the default bridge);
//   - the backends in state.json are re-pushed to the new Caddy.
//
// State is loaded under the lock before the container is touched, so a
// corrupt state.json aborts without any container mutation.
func (a *App) recreateCaddy(ctx context.Context, image string) error {
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

	info, err := a.docker.InspectContainer(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	if err := a.docker.Remove(CaddyContainerName); err != nil {
		return err
	}
	if err := a.ensureCaddyVolumes(); err != nil {
		return err
	}
	if err := a.docker.CreateAndStart(caddyContainerSpec(image, a.hostNetwork())); err != nil {
		return err
	}
	for _, n := range info.Networks {
		switch n {
		case "bridge", "host", "none":
			// Attached by `docker run` itself, or not attachable.
			continue
		}
		if err := a.docker.ConnectNetwork(n, CaddyContainerName); err != nil {
			return fmt.Errorf("reattach network %s: %w", n, err)
		}
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return fmt.Errorf("attach shared network: %w", err)
	}

	// Restore routes before the relay: a relay failure then leaves only
	// host-mode backends down (and is reported), instead of a Caddy with no
	// routes at all that a re-run of install would consider up to date.
	if err := a.waitForAdmin(ctx); err != nil {
		return fmt.Errorf("recreated container but the caddy admin API did not come up; run 'caddy_shack status': %w", err)
	}
	if err := a.caddy.Load(ctx, a.buildConfig(s)); err != nil {
		return fmt.Errorf("recreated container but failed to restore backends; run 'caddy_shack status': %w", err)
	}
	if err := a.ensureRelay(); err != nil {
		return fmt.Errorf("recreated container and restored backends, but the relay failed; run 'caddy_shack doctor': %w", err)
	}
	return nil
}
