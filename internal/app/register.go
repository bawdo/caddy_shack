package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// RegisterInput holds the fields the register command accepts.
type RegisterInput struct {
	Name     string
	Domain   string // optional; defaults to <Name>.test
	Upstream string
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Register adds or updates a backend. Idempotent: identical re-registration
// is a no-op (no Caddy push, no state write).
func (a *App) Register(ctx context.Context, in RegisterInput) error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match ^[a-z0-9][a-z0-9-]{0,30}$ (got %q)", ErrInvalidArgs, in.Name)
	}
	if err := validateUpstream(in.Upstream); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArgs, err)
	}
	if in.Domain == "" {
		in.Domain = in.Name + ".test"
	}

	// Caddy must be running before we attempt any state change.
	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return err
	}

	// Domain-conflict check (skipping the same name's own row).
	for name, b := range s.Backends {
		if name != in.Name && b.Domain == in.Domain {
			return fmt.Errorf("%w: %s already owned by backend %q", ErrDomainConflict, in.Domain, name)
		}
	}

	existing, present := s.Backends[in.Name]
	if present && existing.Domain == in.Domain && existing.Upstream == in.Upstream {
		// Identical re-registration. No-op.
		return nil
	}

	now := time.Now().Format(time.RFC3339)
	s.Backends[in.Name] = state.Backend{
		Domain:       in.Domain,
		Upstream:     in.Upstream,
		RegisteredAt: now,
	}

	cfg := caddyapi.Build(s)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return err
	}

	if err := state.Save(a.paths.StatePath, s); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// validateUpstream checks the host:port shape. Hostname is anything
// net.SplitHostPort accepts; port must be 1-65535.
func validateUpstream(u string) error {
	host, portStr, err := net.SplitHostPort(u)
	if err != nil {
		return fmt.Errorf("upstream must be host:port (got %q): %w", u, err)
	}
	if host == "" {
		return fmt.Errorf("upstream host is empty")
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("upstream port out of range (got %q)", portStr)
	}
	return nil
}
