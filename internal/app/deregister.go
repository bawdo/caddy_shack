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

// Deregister removes a backend. Idempotent: unknown name returns
// (false, nil) and makes no Caddy call. The bool is true when a backend
// was actually removed, false when the name was unknown - callers use it
// to render the right user-facing message.
func (a *App) Deregister(ctx context.Context, name string) (bool, error) {
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return false, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return false, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return false, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return false, err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return false, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return false, err
	}

	if _, ok := s.Backends[name]; !ok {
		return false, nil
	}

	// Work on a copy so a Caddy failure leaves on-disk state intact.
	updated := *s
	updated.Backends = make(map[string]state.Backend, len(s.Backends))
	for k, v := range s.Backends {
		if k != name {
			updated.Backends[k] = v
		}
	}

	cfg := caddyapi.Build(&updated)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return false, fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return false, err
	}

	if err := state.Save(a.paths.StatePath, &updated); err != nil {
		return false, fmt.Errorf("save state: %w", err)
	}
	return true, nil
}
