package app

import (
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
// count. Returns ErrUnhealthy when not Healthy so cmd layer maps it to
// exit 1.
func (a *App) Status() (StatusReport, error) {
	rep := StatusReport{}
	if cs, err := a.docker.ContainerState(CaddyContainerName); err == nil {
		rep.ContainerState = cs.String()
	} else {
		rep.ContainerState = docker.StateAbsent.String()
	}

	rep.AdminReachable = a.caddy.Reachable()

	if s, err := state.Load(a.paths.StatePath); err == nil {
		rep.BackendCount = len(s.Backends)
	}

	rep.Healthy = rep.ContainerState == docker.StateRunning.String() && rep.AdminReachable
	if !rep.Healthy {
		return rep, ErrUnhealthy
	}
	return rep, nil
}
