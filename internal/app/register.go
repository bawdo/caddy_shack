package app

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// UpstreamHost is the in-container DNS name that resolves to the host
// running Docker. All backends route through this; the user only supplies
// the port their service listens on. host.docker.internal is provided by
// Docker Desktop on macOS automatically.
const UpstreamHost = "host.docker.internal"

// RegisterInput holds the fields the register command accepts. Port is the
// host-side TCP port the user's service listens on; the upstream Caddy
// reverse-proxies to is constructed as host.docker.internal:<port>.
type RegisterInput struct {
	Name   string
	Domain string // optional; defaults to <Name>.test
	Port   int
}

// nameRE accepts one or more dot-separated DNS labels. A single label (foo)
// maps to foo.test; a dotted name (bar.foo) maps to bar.foo.test and acts as
// a subdomain override. Each label keeps the original single-label rules.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}(\.[a-z0-9][a-z0-9-]{0,30})*$`)

// Register adds or updates a backend. Idempotent: identical re-registration
// is a no-op (no Caddy push, no state write).
func (a *App) Register(ctx context.Context, in RegisterInput) error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match ^[a-z0-9][a-z0-9-]{0,30}(\\.[a-z0-9][a-z0-9-]{0,30})*$ (got %q)", ErrInvalidArgs, in.Name)
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("%w: port out of range (got %d, must be 1-65535)", ErrInvalidArgs, in.Port)
	}
	if in.Domain == "" {
		in.Domain = in.Name + ".test"
	}
	upstream := fmt.Sprintf("%s:%d", UpstreamHost, in.Port)

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
	if present && existing.Domain == in.Domain && existing.Upstream == upstream {
		// Identical re-registration. No-op.
		return nil
	}

	now := time.Now().Format(time.RFC3339)
	s.Backends[in.Name] = state.Backend{
		Domain:       in.Domain,
		Upstream:     upstream,
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
