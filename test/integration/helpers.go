//go:build integration

// Package integration spins up real CaddyShack-managed Caddy containers
// against a temporary state directory. Tests in this package require the
// `docker` CLI on PATH and a running docker daemon. Run with:
//   go test -tags integration ./test/integration/...
package integration

import (
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/docker"
)

const testContainerName = "caddy_shack_it"

// SkipUnlessDocker skips the test if `docker info` doesn't succeed.
func SkipUnlessDocker(t *testing.T) {
	t.Helper()
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker unavailable: %v", err)
	}
}

// CleanupContainer ensures the test container is gone before AND after the
// test. The container name is fixed to keep tests isolated from a developer's
// real CaddyShack install.
func CleanupContainer(t *testing.T) {
	t.Helper()
	rm := func() {
		_ = exec.Command("docker", "rm", "-f", testContainerName).Run()
	}
	rm()
	t.Cleanup(rm)
}

// WaitForCaddyAdmin polls the admin API until /config/ returns 2xx or the
// deadline expires.
func WaitForCaddyAdmin(t *testing.T, baseURL string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/config/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("Caddy admin API never came up at %s", baseURL)
}

// MustAppWithTempState builds an App pointed at a tempdir state file. The
// caller is responsible for ensuring Caddy is running on the admin port the
// App was built with.
func MustAppWithTempState(t *testing.T, statePath string) *app.App {
	t.Helper()
	return app.NewWithPaths(app.Paths{
		StatePath: statePath,
		LockPath:  statePath + ".lock",
	}, nil, nil)
}

// fakeRunningDocker is a stub Client for integration tests that own the
// real container lifecycle themselves (so they don't want App to touch
// docker for inspections). Always reports the container as running.
type fakeRunningDocker struct{}

func (fakeRunningDocker) DaemonReachable() error                                { return nil }
func (fakeRunningDocker) EnsureVolume(string) error                             { return nil }
func (fakeRunningDocker) PullImage(string) error                                { return nil }
func (fakeRunningDocker) ContainerState(string) (docker.ContainerState, error)  { return docker.StateRunning, nil }
func (fakeRunningDocker) CreateAndStart(docker.ContainerSpec) error             { return nil }
func (fakeRunningDocker) Start(string) error                                    { return nil }
func (fakeRunningDocker) Stop(string) error                                     { return nil }
