package app

import "context"

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
