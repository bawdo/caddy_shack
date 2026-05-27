package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/bawdo/caddy_shack/internal/state"
)

// Listing is one row in the output of List.
type Listing struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
}

// List returns the registered backends, sorted by name. ctx is honoured at
// entry; the rest of the function is in-memory + a single state file read,
// so finer-grained cancellation has no real value.
func (a *App) List(ctx context.Context) ([]Listing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) {
			return nil, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return nil, err
	}
	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Listing, 0, len(names))
	for _, n := range names {
		b := s.Backends[n]
		out = append(out, Listing{
			Name: n, Domain: b.Domain, Upstream: b.Upstream, RegisteredAt: b.RegisteredAt,
		})
	}
	return out, nil
}
