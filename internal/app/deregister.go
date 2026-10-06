package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bawdo/caddy_shack/internal/docker"
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
// no children), it writes no state, only pushes to Caddy if its live config
// has drifted from the registry, and returns Removed=false.
func (a *App) Deregister(ctx context.Context, name string, cascade bool) (DeregisterResult, error) {
	var res DeregisterResult

	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return res, fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return res, fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}

	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return res, err
	}
	defer func() {
		_ = lock.Release()
	}()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
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
		// Nothing to remove: parent absent and (no cascade, or no
		// children). The registry is unchanged, but Caddy is still made to
		// match it.
		_, err := a.reconcileLocked(ctx, s)
		return res, err
	}

	updated := s.Clone()
	for k := range remove {
		delete(updated.Backends, k)
	}
	if err := a.commit(ctx, s, updated); err != nil {
		return res, err
	}

	res.Removed = parentPresent
	return res, nil
}
