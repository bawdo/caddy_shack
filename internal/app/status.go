package app

import (
	"context"
	"errors"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// StatusReport is what `status` returns. Marshalled with --json.
type StatusReport struct {
	ContainerState string `json:"container_state"`
	AdminReachable bool   `json:"admin_reachable"`
	BackendCount   int    `json:"backend_count"`
	Healthy        bool   `json:"healthy"`
	// CaddyVersion is the running Caddy version (e.g. "v2.8.4"). Empty when
	// the container is not running or the probe was unavailable.
	CaddyVersion string `json:"caddy_version,omitempty"`
	// RelayState is the host-mode relay container's state on native Linux;
	// empty on every other engine, which has no relay.
	RelayState string `json:"relay_state,omitempty"`
	// Drift compares Caddy's live config (GET /config/) with the registry.
	// Nil when it could not be checked: the container or admin API is
	// down, the state file is unreadable, or the live config could not be
	// read (see DriftError).
	Drift *Drift `json:"drift,omitempty"`
	// DriftError says why a running, reachable Caddy's config could not be
	// compared with the registry. Status is unhealthy when it is set.
	DriftError string `json:"drift_error,omitempty"`
}

// Status reports container state, admin API reachability, backend count,
// and whether Caddy's live config matches the registry. It only reads:
// fixing drift is 'caddy_shack sync'. Returns:
//   - ErrDockerUnreachable when the docker daemon is down (exit 4)
//   - ErrStateCorrupt when state.json is unparseable (exit 7)
//   - ErrUnhealthy when the container, admin API, or relay is down, or
//     the live config could not be compared (exit 1)
//   - ErrDrift when everything is up but the live config does not match
//     the registry (exit 9)
//   - nil when healthy
//
// The report is populated as much as possible even when an error is returned,
// so the cmd layer can print partial diagnostic info before exiting. The ctx
// is passed to the Caddy admin reachability probe so a Ctrl+C during a
// hanging check exits promptly.
func (a *App) Status(ctx context.Context) (StatusReport, error) {
	rep := StatusReport{}
	var priorityErr error

	cs, err := a.docker.ContainerState(a.inst.ContainerName)
	switch {
	case errors.Is(err, docker.ErrDaemonUnreachable):
		rep.ContainerState = "docker-down"
		priorityErr = ErrDockerUnreachable
	case err != nil:
		rep.ContainerState = docker.StateAbsent.String()
	default:
		rep.ContainerState = cs.String()
	}

	rep.AdminReachable = a.caddy.Reachable(ctx)

	// Best-effort version probe; only meaningful while the container runs.
	if rep.ContainerState == docker.StateRunning.String() {
		rep.CaddyVersion = a.caddyVersion(ctx)
	}

	s, err := state.Load(a.paths.StatePath)
	if err != nil {
		if state.IsCorrupt(err) && priorityErr == nil {
			priorityErr = ErrStateCorrupt
		}
		s = nil
	} else {
		rep.BackendCount = len(s.Backends)
	}

	if priorityErr == nil && a.hostNetwork().Kind == EngineNativeLinux {
		if rs, err := a.docker.ContainerState(a.inst.RelayContainerName); err != nil {
			rep.RelayState = "unknown"
		} else {
			rep.RelayState = rs.String()
		}
	}

	up := rep.ContainerState == docker.StateRunning.String() && rep.AdminReachable
	if up && s != nil {
		if d, err := a.liveDrift(ctx, s); err != nil {
			rep.DriftError = err.Error()
		} else {
			rep.Drift = &d
		}
	}

	servicesUp := up && (rep.RelayState == "" || rep.RelayState == docker.StateRunning.String())
	inSync := rep.Drift != nil && rep.Drift.InSync
	rep.Healthy = servicesUp && inSync

	switch {
	case priorityErr != nil:
		return rep, priorityErr
	case !servicesUp || rep.Drift == nil:
		return rep, ErrUnhealthy
	case !inSync:
		return rep, ErrDrift
	}
	return rep, nil
}
