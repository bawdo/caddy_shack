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
}

// Status reports container state, admin API reachability, and backend
// count. Returns:
//   - ErrDockerUnreachable when the docker daemon is down (exit 4)
//   - ErrStateCorrupt when state.json is unparseable (exit 7)
//   - ErrUnhealthy when running+reachable check fails (exit 1)
//   - nil when healthy
//
// The report is populated as much as possible even when an error is returned,
// so the cmd layer can print partial diagnostic info before exiting. The ctx
// is passed to the Caddy admin reachability probe so a Ctrl+C during a
// hanging check exits promptly.
func (a *App) Status(ctx context.Context) (StatusReport, error) {
	rep := StatusReport{}
	var priorityErr error

	cs, err := a.docker.ContainerState(CaddyContainerName)
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

	if s, err := state.Load(a.paths.StatePath); err != nil {
		if state.IsCorrupt(err) && priorityErr == nil {
			priorityErr = ErrStateCorrupt
		}
	} else {
		rep.BackendCount = len(s.Backends)
	}

	if priorityErr == nil && a.hostNetwork().Kind == EngineNativeLinux {
		if rs, err := a.docker.ContainerState(RelayContainerName); err != nil {
			rep.RelayState = "unknown"
		} else {
			rep.RelayState = rs.String()
		}
	}

	rep.Healthy = rep.ContainerState == docker.StateRunning.String() && rep.AdminReachable &&
		(rep.RelayState == "" || rep.RelayState == docker.StateRunning.String())

	if priorityErr != nil {
		return rep, priorityErr
	}
	if !rep.Healthy {
		return rep, ErrUnhealthy
	}
	return rep, nil
}
