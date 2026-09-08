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
	// CertCount is the number of certificates recorded in state.json,
	// covering both internal-CA and Let's Encrypt certificates.
	CertCount int `json:"cert_count"`
	// SoonestCertExpiryDays is the number of days until the nearest
	// certificate expiry, truncated toward zero (0 means "expiring or just
	// expired"; negative means already expired). It is nil when there are
	// no certificates, or when none of the stored expiries could be parsed,
	// so a zero value in the JSON can never be mistaken for "expires
	// today" when no certificate exists at all. Renewal is manual, so this
	// is the only place an approaching expiry surfaces outside `cert list`.
	SoonestCertExpiryDays *int `json:"soonest_cert_expiry_days,omitempty"`
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
		rep.CertCount = len(s.Certs)
		var soonest *int
		for _, c := range s.Certs {
			d, ok := daysRemaining(c.NotAfter)
			if !ok {
				continue
			}
			if soonest == nil || d < *soonest {
				soonest = &d
			}
		}
		rep.SoonestCertExpiryDays = soonest
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
