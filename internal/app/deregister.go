package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// DeregisterResult reports what Deregister did so the command layer can render
// the right messages.
type DeregisterResult struct {
	// Removed is true when the named parent backend existed and was removed.
	Removed bool
	// ParentDomain is the domain the children suffix was computed against
	// (the parent's stored domain, or <name>.test when the parent is absent).
	ParentDomain string
	// Cascaded lists child backend names removed because cascade was set
	// (sorted).
	Cascaded []string
	// ChildrenLeft lists child backend names left in place because cascade
	// was not set (sorted).
	ChildrenLeft []string
}

// Deregister removes a backend. With cascade=false it removes only the named
// backend, leaving any subdomain backends under it in place (their names are
// reported in ChildrenLeft). With cascade=true it also removes every backend
// whose domain is a subdomain of the parent's domain.
//
// Idempotent: when nothing matches (unknown name and either cascade=false or
// no children), it makes no Caddy call and returns Removed=false.
func (a *App) Deregister(ctx context.Context, name string, cascade bool) (DeregisterResult, error) {
	var res DeregisterResult

	st, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return res, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return res, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return res, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return res, err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return res, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return res, err
	}

	parent, parentPresent := s.Backends[name]
	parentDomain := name + ".test"
	if parentPresent {
		parentDomain = parent.Domain
	}
	res.ParentDomain = parentDomain
	childSuffix := "." + parentDomain

	children := make([]string, 0)
	for k, v := range s.Backends {
		if k != name && strings.HasSuffix(v.Domain, childSuffix) {
			children = append(children, k)
		}
	}
	sort.Strings(children)

	remove := make(map[string]bool)
	if parentPresent {
		remove[name] = true
	}
	if cascade {
		for _, c := range children {
			remove[c] = true
		}
		res.Cascaded = children
	} else {
		res.ChildrenLeft = children
	}

	if len(remove) == 0 {
		// Nothing to remove: parent absent and (no cascade, or no children).
		return res, nil
	}

	// Work on a copy so a Caddy failure leaves on-disk state intact.
	updated := *s
	updated.Backends = make(map[string]state.Backend, len(s.Backends))
	for k, v := range s.Backends {
		if !remove[k] {
			updated.Backends[k] = v
		}
	}

	certs, err := a.loadedCerts(&updated)
	if err != nil {
		return res, err
	}
	cfg := caddyapi.Build(&updated, certs)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return res, fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return res, err
	}

	if err := state.Save(a.paths.StatePath, &updated); err != nil {
		return res, fmt.Errorf("save state: %w", err)
	}

	res.Removed = parentPresent
	return res, nil
}
