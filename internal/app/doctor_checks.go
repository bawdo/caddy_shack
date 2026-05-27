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
