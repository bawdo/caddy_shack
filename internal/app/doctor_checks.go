package app

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func checkDockerDaemon(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "docker-daemon",
			Status:  SeverityFail,
			Summary: "Docker daemon unreachable",
			Detail:  []string{err.Error(), "start Docker Desktop and re-run"},
		}
	}
	return CheckResult{
		Name:    "docker-daemon",
		Status:  SeverityPass,
		Summary: "Docker reachable",
	}
}

func checkContainerHealth(ctx context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeveritySkip,
			Summary: "docker daemon unreachable",
		}
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "cannot inspect container",
			Detail:  []string{err.Error()},
		}
	}
	switch state {
	case docker.StateAbsent:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container not installed",
			Fix: &FixAction{
				Label: "install Caddy container ('caddy_shack install')",
				Apply: func(_ context.Context) error { return a.Install() },
			},
		}
	case docker.StateStopped:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container stopped",
			Fix: &FixAction{
				Label: "start Caddy container ('caddy_shack start')",
				Apply: func(_ context.Context) error { return a.Start() },
			},
		}
	}
	if !a.caddy.Reachable(ctx) {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "container running but admin API unreachable",
			Detail:  []string{"check 'docker logs caddy_shack' for Caddy startup errors"},
			Fix: &FixAction{
				Label: "restart container ('caddy_shack restart')",
				Apply: func(_ context.Context) error { return a.Restart() },
			},
		}
	}
	return CheckResult{
		Name:    "container-health",
		Status:  SeverityPass,
		Summary: "running; admin API responding",
	}
}

// checkBackends is implemented in Task 13.
func checkBackends(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "backends", Status: SeveritySkip, Summary: "not implemented"}
}

// doctorPorts is the list of loopback ports CaddyShack publishes from the
// container. Kept package-private so the check has a single source of truth.
var doctorPorts = []int{80, 443, 2019}

func checkPortAvailability(ctx context.Context, a *App) CheckResult {
	return checkPortAvailabilityOn(ctx, a, doctorPorts)
}

// checkPortAvailabilityOn is the test seam. Tests call it with a free
// ephemeral port so they don't need root for 80/443.
func checkPortAvailabilityOn(_ context.Context, a *App, ports []int) CheckResult {
	var taken []string
	for _, p := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			taken = append(taken, fmt.Sprintf("port %d held: %v", p, err))
			continue
		}
		_ = l.Close()
	}
	if len(taken) == 0 {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "80, 443, 2019 free or held by caddy_shack",
		}
	}
	// If our container is running, assume it holds the ports (the spec's
	// "free or already held by caddy_shack" wording). PASS with a note.
	state, _ := a.docker.ContainerState(CaddyContainerName)
	if state == docker.StateRunning {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "ports held by caddy_shack (container running)",
		}
	}
	return CheckResult{
		Name:    "port-availability",
		Status:  SeverityFail,
		Summary: "required loopback ports are in use by a foreign process",
		Detail:  append(taken, "stop the process holding the port, or run 'caddy_shack stop' if you have another caddy_shack install"),
	}
}
