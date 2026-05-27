package app

import (
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
}

// Status reports container state, admin API reachability, and backend
// count. Returns:
//   - ErrDockerUnreachable when the docker daemon is down (exit 4)
//   - ErrStateCorrupt when state.json is unparseable (exit 7)
//   - ErrUnhealthy when running+reachable check fails (exit 1)
//   - nil when healthy
//
// The report is populated as much as possible even when an error is returned,
// so the cmd layer can print partial diagnostic info before exiting.
func (a *App) Status() (StatusReport, error) {
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

	rep.AdminReachable = a.caddy.Reachable()

	if s, err := state.Load(a.paths.StatePath); err != nil {
		if state.IsCorrupt(err) && priorityErr == nil {
			priorityErr = ErrStateCorrupt
		}
	} else {
		rep.BackendCount = len(s.Backends)
	}

	rep.Healthy = rep.ContainerState == docker.StateRunning.String() && rep.AdminReachable

	if priorityErr != nil {
		return rep, priorityErr
	}
	if !rep.Healthy {
		return rep, ErrUnhealthy
	}
	return rep, nil
}
