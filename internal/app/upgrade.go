package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// adminReadyTimeout and adminReadyInterval bound the wait for the Caddy
// admin API to come back up after a recreate. Package vars so tests can
// shrink them; an already-reachable API returns on the first poll.
var (
	adminReadyTimeout  = 30 * time.Second
	adminReadyInterval = 250 * time.Millisecond
)

// UpgradeReport is the outcome of an upgrade; the cmd layer renders it.
type UpgradeReport struct {
	// AlreadyCurrent is true when the running container already used the
	// freshly-pulled image, so nothing was recreated.
	AlreadyCurrent bool
	// Version is the running Caddy version (e.g. "v2.8.4"), or "" when the
	// probe was unavailable.
	Version string
}

// Upgrade re-pulls the pinned Caddy image and, if it differs from the image
// the current container runs, recreates the container against it (preserving
// both named volumes), then re-pushes the registered backends from state so
// every .test URL keeps working. If the pulled image matches what is already
// running, it reports AlreadyCurrent and leaves the container untouched.
func (a *App) Upgrade(ctx context.Context) (UpgradeReport, error) {
	if err := a.docker.DaemonReachable(); err != nil {
		return UpgradeReport{}, fmt.Errorf("%w: %v", ErrDockerUnreachable, err)
	}

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("inspect container: %w", err)
	}
	if st == docker.StateAbsent {
		return UpgradeReport{}, fmt.Errorf("%w: run 'caddy_shack install' first", ErrNotInstalled)
	}

	if err := a.docker.PullImage(CaddyImage); err != nil {
		return UpgradeReport{}, err
	}

	newID, err := a.docker.ImageID(CaddyImage)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("image id: %w", err)
	}
	curID, err := a.docker.ContainerImageID(CaddyContainerName)
	if err != nil {
		return UpgradeReport{}, fmt.Errorf("container image id: %w", err)
	}
	if newID == curID {
		return UpgradeReport{AlreadyCurrent: true, Version: a.caddyVersion(ctx)}, nil
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return UpgradeReport{}, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return UpgradeReport{}, err
	}
	defer func() { _ = lock.Release() }()

	// Load backends before touching the container so a corrupt state aborts
	// without any container mutation.
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return UpgradeReport{}, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return UpgradeReport{}, err
	}

	if err := a.docker.Remove(CaddyContainerName); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.EnsureVolume(CaddyDataVolume); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.EnsureVolume(CaddyConfigVolume); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.docker.CreateAndStart(caddyContainerSpec(CaddyImage)); err != nil {
		return UpgradeReport{}, err
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return UpgradeReport{}, fmt.Errorf("attach shared network: %w", err)
	}

	if err := a.waitForAdmin(ctx); err != nil {
		return UpgradeReport{}, fmt.Errorf("recreated container but the caddy admin API did not come up; run 'caddy_shack status': %w", err)
	}
	if err := a.caddy.Load(ctx, caddyapi.Build(s, nil)); err != nil {
		return UpgradeReport{}, fmt.Errorf("recreated container but failed to restore backends; run 'caddy_shack status': %w", err)
	}

	return UpgradeReport{AlreadyCurrent: false, Version: a.caddyVersion(ctx)}, nil
}

// waitForAdmin polls the Caddy admin API until it is reachable or the bound
// elapses. Honours ctx so Ctrl+C exits promptly.
func (a *App) waitForAdmin(ctx context.Context) error {
	deadline := time.Now().Add(adminReadyTimeout)
	for {
		if a.caddy.Reachable(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("not reachable within %s", adminReadyTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(adminReadyInterval):
		}
	}
}

// caddyVersion returns the running Caddy version via `caddy version` inside
// the container. Best-effort: returns "" when the exec fails or output is
// empty.
func (a *App) caddyVersion(ctx context.Context) string {
	out, err := a.docker.Exec(ctx, CaddyContainerName, []string{"caddy", "version"})
	if err != nil {
		return ""
	}
	return parseCaddyVersion(out)
}

// parseCaddyVersion takes the first whitespace-separated field of
// `caddy version` output (e.g. "v2.8.4 h1:..." -> "v2.8.4").
func parseCaddyVersion(out []byte) string {
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
