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
// running Docker. Host-mode backends are stored as UpstreamHost:<port>; the
// user only supplies the port their service listens on. Docker Desktop and
// VM runtimes provide the name and forward it to the host's loopback, so
// Caddy dials it directly there. On native Linux the stored upstream is
// rendered as a dial through the relay instead (see relay.go), because
// docker0 can't reach loopback-bound services.
const UpstreamHost = "host.docker.internal"

// RegisterInput holds the fields the register command accepts. Port is the
// host-side TCP port the user's service listens on; the upstream is stored
// as host.docker.internal:<port>.
type RegisterInput struct {
	Name   string
	Domain string // optional; defaults to <Name>.test
	Port   int
	// Container, when non-empty, switches to container-routing mode: the
	// upstream dial becomes <Container>:<Port> over the shared network
	// instead of host.docker.internal:<Port>. Requires the caddy container
	// to be attached to CaddyNetworkName (ensured here).
	Container string
	// LAN gives the backend the LAN name <Name>.<LAN suffix>. Mutually
	// exclusive with LANDomain. Needs LAN mode enabled.
	LAN bool
	// LANDomain gives the backend an explicit LAN name. Mutually exclusive
	// with LAN. Needs LAN mode enabled.
	LANDomain string
}

// nameRE accepts one or more dot-separated DNS labels. A single label (foo)
// maps to foo.test; a dotted name (bar.foo) maps to bar.foo.test and acts as
// a subdomain override. Each label keeps the original single-label rules.
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}(\.[a-z0-9][a-z0-9-]{0,30})*$`)

// containerRE validates a Docker network alias supplied via --container. A
// single DNS label keeps it usable as a network alias.
var containerRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Register adds or updates a backend. Idempotent: identical re-registration
// writes no state and only pushes to Caddy if its live config has drifted
// from the registry.
func (a *App) Register(ctx context.Context, in RegisterInput) error {
	if !nameRE.MatchString(in.Name) {
		return fmt.Errorf("%w: name must match ^[a-z0-9][a-z0-9-]{0,30}(\\.[a-z0-9][a-z0-9-]{0,30})*$ (got %q)", ErrInvalidArgs, in.Name)
	}
	if in.Port < 1 || in.Port > 65535 {
		return fmt.Errorf("%w: port out of range (got %d, must be 1-65535)", ErrInvalidArgs, in.Port)
	}
	if in.LAN && in.LANDomain != "" {
		return fmt.Errorf("%w: --lan and --lan-domain are mutually exclusive", ErrInvalidArgs)
	}
	if in.Domain == "" {
		in.Domain = in.Name + ".test"
	}
	var upstream string
	if in.Container != "" {
		if !containerRE.MatchString(in.Container) {
			return fmt.Errorf("%w: container alias must match ^[a-z0-9][a-z0-9-]{0,62}$ (got %q)", ErrInvalidArgs, in.Container)
		}
		upstream = fmt.Sprintf("%s:%d", in.Container, in.Port)
	} else {
		upstream = fmt.Sprintf("%s:%d", UpstreamHost, in.Port)
	}

	// Caddy must be running before we attempt any state change.
	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	// Container mode needs the caddy container attached to the shared
	// network so it can resolve the app container by alias. Host mode never
	// touches the network. Idempotent; safe on repeated registers.
	if in.Container != "" {
		if err := a.ensureSharedNetwork(); err != nil {
			return fmt.Errorf("ensure shared network: %w", err)
		}
	} else {
		a.noteStaleCaddy()
	}

	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}

	// Resolve the LAN name. Neither flag means no LAN name: Register is a
	// full upsert, so re-registering without a flag clears an earlier one.
	var lanDomain string
	if in.LAN || in.LANDomain != "" {
		if !s.LANEnabled() {
			return fmt.Errorf("%w: run 'caddy_shack lan enable' first", ErrLANDisabled)
		}
		if in.LAN {
			lanDomain = in.Name + "." + s.LAN.Suffix
		} else {
			if err := validLANName(in.LANDomain); err != nil {
				return err
			}
			lanDomain = in.LANDomain
		}
	}

	// Domain-conflict check (skipping the same name's own row). Names from
	// the .test and LAN spaces share one namespace, so every pairing counts.
	for name, b := range s.Backends {
		if name == in.Name {
			continue
		}
		for _, mine := range []string{in.Domain, lanDomain} {
			if mine != "" && (b.Domain == mine || b.LANDomain == mine) {
				return fmt.Errorf("%w: %s already owned by backend %q", ErrDomainConflict, mine, name)
			}
		}
	}

	existing, present := s.Backends[in.Name]
	if present && existing.Domain == in.Domain && existing.Upstream == upstream && existing.LANDomain == lanDomain {
		// Identical re-registration: no state write. Caddy is still made
		// to match the registry, so a caller that re-registers on startup
		// gets its routes back after Caddy lost them.
		_, err := a.reconcileLocked(ctx, s)
		return err
	}

	updated := s.Clone()
	updated.Backends[in.Name] = state.Backend{
		Domain:       in.Domain,
		LANDomain:    lanDomain,
		Upstream:     upstream,
		RegisteredAt: time.Now().Format(time.RFC3339),
	}
	return a.commit(ctx, s, updated)
}

// commit pushes updated to Caddy and then writes it as the registry. The
// caller holds the state lock and prev is the registry as loaded under it.
// Caddy goes first so a rejected config never reaches state.json. If the
// state write then fails, Caddy is rolled back to prev so the two don't
// diverge; if that rollback fails too, the error says so.
func (a *App) commit(ctx context.Context, prev, updated *state.State) error {
	if err := a.caddy.Load(ctx, a.buildConfig(updated)); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return err
	}
	if err := state.Save(a.paths.StatePath, updated); err != nil {
		if rbErr := a.caddy.Load(ctx, a.buildConfig(prev)); rbErr != nil {
			return fmt.Errorf("save state: %w; caddy now serves the change but the registry does not have it, "+
				"and rolling caddy back failed (%v); fix the state file, then run 'caddy_shack sync'", err, rbErr)
		}
		return fmt.Errorf("save state: %w (caddy rolled back to the registry)", err)
	}
	return nil
}
