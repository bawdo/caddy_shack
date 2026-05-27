package app

import "context"

// checkDockerDaemon is implemented in Task 6.
func checkDockerDaemon(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "docker-daemon", Status: SeveritySkip, Summary: "not implemented"}
}

// checkBackends is implemented in Task 13.
func checkBackends(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "backends", Status: SeveritySkip, Summary: "not implemented"}
}
