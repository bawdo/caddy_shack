package app

import (
	"context"
	"fmt"
	"strings"
)

// ensureCaddyVolumes creates every named volume the Caddy container mounts
// on this engine. Idempotent.
func (a *App) ensureCaddyVolumes() error {
	for _, v := range caddyContainerSpec(a.inst, CaddyImage, a.hostNetwork()).Volumes {
		if err := a.docker.EnsureVolume(v.VolumeName); err != nil {
			return err
		}
	}
	return nil
}

// caddyDrift lists what the existing Caddy container lacks compared with the
// spec this engine needs (see specDrift). Empty means up to date. Checked on
// every engine: native Linux needs the relay socket and host-gateway, and
// every engine needs the boot command that resumes Caddy's autosave.
func (a *App) caddyDrift() ([]string, error) {
	info, err := a.docker.InspectContainer(a.inst.ContainerName)
	if err != nil {
		return nil, fmt.Errorf("inspect container: %w", err)
	}
	return specDrift(caddyContainerSpec(a.inst, CaddyImage, a.hostNetwork()), info), nil
}

// noteStaleCaddy points the user at `install` when the existing container
// predates the settings this version needs. Used by commands that must not
// recreate the container themselves (start, register). Best-effort and
// silent on inspection errors.
func (a *App) noteStaleCaddy() {
	drift, err := a.caddyDrift()
	if err != nil || len(drift) == 0 {
		return
	}
	a.notef("%s was created without settings this version needs on the %s engine (%s); until it is recreated, "+
		"host-mode backends may be unreachable and routes are lost whenever the container restarts outside caddy_shack. "+
		"Run: caddy_shack install (keeps volumes, the local CA, networks and backends)\n",
		a.inst.ContainerName, a.hostNetwork().Kind, strings.Join(drift, "; "))
}

// recreateCaddy replaces the Caddy container with one built from image and
// this engine's spec, then restores everything a fresh `docker run` would
// lose:
//   - the data and config volumes are reused as-is, so the local root CA
//     (and every browser/OS trust of it) and issued certs survive;
//   - every network the old container was attached to is reattached
//     (`docker run` only attaches the caddy_shack network it is created
//     on), except the default bridge, which is dropped on purpose;
//   - the backends in state.json are re-pushed to the new Caddy.
//
// State is loaded under the lock before the container is touched, so a
// corrupt state.json aborts without any container mutation.
// The lock is then held across the admin and relay waits (up to 45s) on
// purpose, so a concurrent command gets exit 8 rather than reconciling
// against a half-recreated container.
func (a *App) recreateCaddy(ctx context.Context, image string) error {
	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}

	info, err := a.docker.InspectContainer(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}

	if err := a.docker.Remove(a.inst.ContainerName); err != nil {
		return err
	}
	if err := a.ensureCaddyVolumes(); err != nil {
		return err
	}
	// The container is created on this network, so it must exist first.
	if err := a.docker.EnsureNetwork(a.inst.NetworkName); err != nil {
		return err
	}
	if err := a.docker.CreateAndStart(caddyContainerSpec(a.inst, image, a.hostNetwork())); err != nil {
		return err
	}
	for _, n := range info.Networks {
		switch n {
		case "bridge", "host", "none":
			// The default bridge is left behind on purpose (a plain
			// `docker run` container must not reach the admin API); host
			// and none are not attachable. The caddy_shack network the
			// container was created on comes round too, and reconnecting
			// to it is a no-op (ConnectNetwork treats already-attached as
			// success).
			continue
		}
		if err := a.docker.ConnectNetwork(n, a.inst.ContainerName); err != nil {
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
	cfg, err := a.buildConfig(s)
	if err != nil {
		return fmt.Errorf("recreated container but failed to load certificates; run 'caddy_shack status': %w", err)
	}
	if err := a.caddy.Load(ctx, cfg); err != nil {
		return fmt.Errorf("recreated container but failed to restore backends; run 'caddy_shack status': %w", err)
	}
	if err := a.ensureRelay(ctx); err != nil {
		return fmt.Errorf("recreated container and restored backends, but the relay failed; run 'caddy_shack doctor': %w", err)
	}
	return nil
}
