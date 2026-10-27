package app

import (
	"context"
	"slices"
	"sort"
	"time"
)

// Liveness values for Listing.Live.
const (
	LiveYes     = "yes"     // Caddy serves the backend as registered
	LiveNo      = "no"      // Caddy has no route for the backend
	LiveStale   = "stale"   // Caddy's routes for the backend differ from the registry
	LiveUnknown = "unknown" // Caddy's live config could not be read
)

// listLiveTimeout bounds List's best-effort read of Caddy's live config.
const listLiveTimeout = 2 * time.Second

// Listing is one row in the output of List.
type Listing struct {
	Name         string `json:"name"`
	Domain       string `json:"domain"`
	LANDomain    string `json:"lan_domain,omitempty"`
	Upstream     string `json:"upstream"`
	RegisteredAt string `json:"registered_at"`
	// Live says whether Caddy is actually serving the backend. The
	// registry is what list reports; Live is how a script can tell a
	// registered route from a live one.
	Live string `json:"live"`
}

// List returns the registered backends, sorted by name, each marked with
// whether Caddy is serving it. The live check is best-effort: when Caddy
// can't be read every row is LiveUnknown and List still succeeds.
func (a *App) List(ctx context.Context) ([]Listing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return nil, err
	}
	liveCtx, cancel := context.WithTimeout(ctx, listLiveTimeout)
	defer cancel()
	drift, driftErr := a.liveDrift(liveCtx, s)

	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Listing, 0, len(names))
	for _, n := range names {
		b := s.Backends[n]
		live := LiveYes
		switch {
		case driftErr != nil:
			live = LiveUnknown
		case slices.Contains(drift.Missing, n):
			live = LiveNo
		case slices.Contains(drift.Changed, n):
			live = LiveStale
		}
		out = append(out, Listing{
			Name: n, Domain: b.Domain, LANDomain: b.LANDomain, Upstream: b.Upstream, RegisteredAt: b.RegisteredAt, Live: live,
		})
	}
	return out, nil
}
