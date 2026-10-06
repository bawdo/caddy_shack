package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// The registry (state.json) is the source of truth; Caddy's live config is
// derived from it. These helpers compare the two and, when they differ,
// re-push the registry. Every command that talks to Caddy under the state
// lock goes through them, so a Caddy that lost or changed its config
// (restarted outside caddy_shack, edited through the admin API, or left
// behind by a half-finished command) is put right by the next command
// instead of silently serving the wrong thing.

// Drift is the difference between Caddy's live config and the config the
// registry renders. The zero value is not meaningful; build it with
// compareConfig.
type Drift struct {
	// InSync is true when the live config is exactly what the registry
	// renders (or, with nothing registered, when Caddy has no routes).
	InSync bool `json:"in_sync"`
	// HTTPApp and TLSApp report whether the live config has Caddy's http
	// and tls apps at all. A Caddy that booted from the bare admin-only
	// Caddyfile has neither, so it serves nothing on 80/443.
	HTTPApp bool `json:"http_app"`
	TLSApp  bool `json:"tls_app"`
	// Missing lists registered backends with no live route for their
	// domain or its wildcard (sorted).
	Missing []string `json:"missing,omitempty"`
	// Changed lists registered backends that are partly live, or whose
	// live routes differ from what the registry renders, e.g. a different
	// upstream (sorted).
	Changed []string `json:"changed,omitempty"`
	// Unknown lists live route hosts no registered backend accounts for
	// (sorted).
	Unknown []string `json:"unknown,omitempty"`
}

// compareConfig diffs the live config against what s renders to. desired
// must be a.buildConfig(s).
func compareConfig(s *state.State, live, desired []byte) (Drift, error) {
	var liveCfg, desiredCfg map[string]any
	if err := json.Unmarshal(live, &liveCfg); err != nil {
		return Drift{}, fmt.Errorf("parse live caddy config: %w", err)
	}
	if err := json.Unmarshal(desired, &desiredCfg); err != nil {
		return Drift{}, fmt.Errorf("parse rendered caddy config: %w", err)
	}

	apps, _ := liveCfg["apps"].(map[string]any)
	d := Drift{HTTPApp: apps["http"] != nil, TLSApp: apps["tls"] != nil}

	liveRoutes := routesByHost(liveCfg)
	wantRoutes := routesByHost(desiredCfg)

	names := make([]string, 0, len(s.Backends))
	for n := range s.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		domain := s.Backends[n].Domain
		present, same := 0, true
		for _, host := range []string{domain, "*." + domain} {
			got, ok := liveRoutes[host]
			if ok {
				present++
			}
			if !ok || !reflect.DeepEqual(got, wantRoutes[host]) {
				same = false
			}
		}
		switch {
		case present == 0:
			d.Missing = append(d.Missing, n)
		case !same:
			d.Changed = append(d.Changed, n)
		}
	}
	for host := range liveRoutes {
		if _, ok := wantRoutes[host]; !ok {
			d.Unknown = append(d.Unknown, host)
		}
	}
	sort.Strings(d.Unknown)

	// With nothing registered there is nothing to serve, so a Caddy that
	// has no routes (e.g. freshly booted, or freshly installed) is in sync
	// even though it lacks the empty http/tls apps the registry renders.
	if len(s.Backends) == 0 && len(liveRoutes) == 0 {
		d.InSync = true
	} else {
		d.InSync = reflect.DeepEqual(liveCfg, desiredCfg)
	}
	return d, nil
}

// routesByHost maps every host matched by a route in any http server of cfg
// to that route.
func routesByHost(cfg map[string]any) map[string]any {
	out := map[string]any{}
	apps, _ := cfg["apps"].(map[string]any)
	httpApp, _ := apps["http"].(map[string]any)
	servers, _ := httpApp["servers"].(map[string]any)
	for _, srv := range servers {
		srvMap, _ := srv.(map[string]any)
		routes, _ := srvMap["routes"].([]any)
		for _, r := range routes {
			route, _ := r.(map[string]any)
			matches, _ := route["match"].([]any)
			for _, m := range matches {
				match, _ := m.(map[string]any)
				hosts, _ := match["host"].([]any)
				for _, h := range hosts {
					if host, ok := h.(string); ok {
						out[host] = route
					}
				}
			}
		}
	}
	return out
}

// liveDrift fetches Caddy's live config and compares it with s.
func (a *App) liveDrift(ctx context.Context, s *state.State) (Drift, error) {
	live, err := a.caddy.GetConfig(ctx)
	if err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return Drift{}, fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return Drift{}, fmt.Errorf("read live caddy config: %w", err)
	}
	return compareConfig(s, live, a.buildConfig(s))
}

// SyncReport is the outcome of Sync.
type SyncReport struct {
	// Before is the drift found before anything was pushed.
	Before Drift `json:"before"`
	// Pushed is true when the registry was re-pushed to Caddy.
	Pushed bool `json:"pushed"`
}

// reconcileLocked makes Caddy's live config match s, pushing only when it
// differs. The caller must hold the state lock, so s can't go stale under
// it.
func (a *App) reconcileLocked(ctx context.Context, s *state.State) (SyncReport, error) {
	d, err := a.liveDrift(ctx, s)
	if err != nil {
		return SyncReport{}, err
	}
	rep := SyncReport{Before: d}
	if d.InSync {
		return rep, nil
	}
	if err := a.caddy.Load(ctx, a.buildConfig(s)); err != nil {
		if errors.Is(err, caddyapi.ErrUnreachable) {
			return rep, fmt.Errorf("%w: %v", ErrCaddyNotRunning, err)
		}
		return rep, fmt.Errorf("push registry to caddy: %w", err)
	}
	rep.Pushed = true
	return rep, nil
}

// reconcile takes the state lock, loads the registry, and makes Caddy match
// it. Caddy's admin API must already be up.
func (a *App) reconcile(ctx context.Context) (SyncReport, error) {
	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return SyncReport{}, err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return SyncReport{}, err
	}
	return a.reconcileLocked(ctx, s)
}

// restoreBackends waits for the admin API to come up, then makes Caddy match
// the registry. Used wherever caddy_shack has just started, or found
// running, a Caddy container: one that booted without (or with a stale)
// autosave has nothing but its bare admin config until this runs.
func (a *App) restoreBackends(ctx context.Context) error {
	if err := a.waitForAdmin(ctx); err != nil {
		return fmt.Errorf("caddy admin API did not come up; run 'caddy_shack status': %w", err)
	}
	if _, err := a.reconcile(ctx); err != nil {
		return fmt.Errorf("failed to restore backends; run 'caddy_shack status': %w", err)
	}
	return nil
}

// Sync makes Caddy's live config match the registry, re-pushing it only
// when the two differ. Returns ErrCaddyNotRunning (exit 5) when the
// container is not running or the admin API is down.
func (a *App) Sync(ctx context.Context) (SyncReport, error) {
	if err := a.requireRunning(); err != nil {
		return SyncReport{}, err
	}
	rep, err := a.reconcile(ctx)
	if err != nil {
		return rep, err
	}
	return rep, a.ensureLANFromState()
}

// requireRunning returns ErrCaddyNotRunning unless the Caddy container is
// running.
func (a *App) requireRunning() error {
	st, err := a.docker.ContainerState(a.inst.ContainerName)
	if err != nil {
		return fmt.Errorf("inspect container: %w", err)
	}
	if st != docker.StateRunning {
		return fmt.Errorf("%w (state=%s)", ErrCaddyNotRunning, st)
	}
	return nil
}

// stateLockTimeout is how long acquireStateLock waits for another holder. A
// variable so tests can shorten it.
var stateLockTimeout = 5 * time.Second

// acquireStateLock takes the state lock with the documented 5s timeout,
// mapping contention to ErrLockContention (exit 8).
func acquireStateLock(path string) (*state.Lock, error) {
	lock, err := state.AcquireLock(path, stateLockTimeout)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return nil, fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return nil, err
	}
	return lock, nil
}

// loadState loads the registry, mapping corruption to ErrStateCorrupt
// (exit 7).
func loadState(path string) (*state.State, error) {
	s, err := state.Load(path)
	if err != nil {
		if state.IsCorrupt(err) {
			return nil, fmt.Errorf("%w: %v", ErrStateCorrupt, err)
		}
		return nil, err
	}
	return s, nil
}
