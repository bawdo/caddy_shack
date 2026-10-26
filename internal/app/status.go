package app

import (
	"context"
	"errors"
	"net/netip"

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
	// LAN is informational: nil when state.json has no LAN block. It never
	// affects Healthy or the exit code.
	LAN *LANStatus `json:"lan,omitempty"`
	// CertCount is the number of Let's Encrypt certificates recorded in
	// state.json. Internal-CA certificates are not tracked there.
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

// LANStatus describes LAN exposure for the status report.
type LANStatus struct {
	Enabled        bool   `json:"enabled"`
	IP             string `json:"ip"`
	Mode           string `json:"mode"`
	Suffix         string `json:"suffix"`
	IPPresent      bool   `json:"ip_present"`
	ForwarderState string `json:"forwarder_state"`
	// DiscoveredIP is set only in discovered mode when the interface's
	// current address differs from IP.
	DiscoveredIP string `json:"discovered_ip,omitempty"`
	LANBackends  int    `json:"lan_backends"`
}

// lanStatus builds the informational LAN block. Resolver or docker failures
// leave the affected fields empty or false; they never fail status.
func (a *App) lanStatus(s *state.State) *LANStatus {
	l := s.LAN
	ls := &LANStatus{Enabled: l.Enabled, IP: l.IP, Mode: string(l.Mode), Suffix: l.Suffix}
	for _, b := range s.Backends {
		if b.LANDomain != "" {
			ls.LANBackends++
		}
	}
	if !l.Enabled {
		return ls
	}
	if fs, err := a.docker.ContainerState(a.inst.LANContainerName); err != nil {
		ls.ForwarderState = "unknown"
	} else {
		ls.ForwarderState = fs.String()
	}
	if ip, err := netip.ParseAddr(l.IP); err == nil {
		if ok, err := a.lan.Present(ip); err == nil {
			ls.IPPresent = ok
		}
	}
	if l.Mode == state.LANModeDiscovered {
		if res, err := a.lan.Discover(); err == nil && res.IP.IsValid() && res.IP.String() != l.IP {
			ls.DiscoveredIP = res.IP.String()
		}
	}
	return ls
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

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		if errors.Is(err, ErrStateCorrupt) && priorityErr == nil {
			priorityErr = ErrStateCorrupt
		}
		s = nil
	} else {
		rep.BackendCount = len(s.Backends)
		if s.LAN != nil {
			rep.LAN = a.lanStatus(s)
		}
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
