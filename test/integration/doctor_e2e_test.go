//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
	dockerpkg "github.com/bawdo/caddy_shack/internal/docker"
)

func TestDoctorHappyPathAfterInstall(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{}, // App's docker probes are irrelevant here
	)

	// Doctor will FAIL on container-health (the fake docker reports
	// running but the App's caddyapi is happy), trust-store (no cert yet),
	// and likely DNS depending on host config. The point of this test is
	// that doctor returns a populated report without crashing.
	rep, err := a.Doctor(context.Background(), app.DoctorOptions{SkipBackends: true})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	if len(rep.Checks) == 0 {
		t.Fatal("expected populated report")
	}
	// Ensure container-health PASSes (admin reachable, container 'running'
	// via fakeRunningDocker, real Caddy on adminURL).
	var ch *app.CheckResult
	for i := range rep.Checks {
		if rep.Checks[i].Name == "container-health" {
			ch = &rep.Checks[i]
			break
		}
	}
	if ch == nil {
		t.Fatal("container-health check missing from report")
	}
	if ch.Status != app.SeverityPass {
		t.Errorf("container-health: status=%v summary=%s", ch.Status, ch.Summary)
	}

	// Cleanup ports
	_ = net.IPv4(0, 0, 0, 0) // keep import
}

// stoppedDocker is a docker.Client that always reports app.CaddyContainerName
// as StateStopped. Used by TestDoctorStoppedContainerReportsFail so the
// test doesn't depend on the test container's actual lifecycle - the doctor
// container-health check sees "stopped" regardless of what's running on
// the real docker daemon.
type stoppedDocker struct{}

func (stoppedDocker) DaemonReachable() error    { return nil }
func (stoppedDocker) EnsureVolume(string) error { return nil }
func (stoppedDocker) PullImage(string) error    { return nil }
func (stoppedDocker) ContainerState(string) (dockerpkg.ContainerState, error) {
	return dockerpkg.StateStopped, nil
}
func (stoppedDocker) CreateAndStart(dockerpkg.ContainerSpec) error { return nil }
func (stoppedDocker) Start(string) error                           { return nil }
func (stoppedDocker) Stop(string) error                            { return nil }
func (stoppedDocker) Remove(string) error                          { return nil }
func (stoppedDocker) Exec(context.Context, string, []string) ([]byte, error) {
	return nil, nil
}

// execOnlyDocker is a docker.Client that only implements Exec against a
// real container; the other methods always report "running" for the App's
// container name. Lets the backends check use real `docker exec` while
// keeping the App's docker probes off our test container's lifecycle.
type execOnlyDocker struct{ container string }

func (execOnlyDocker) DaemonReachable() error    { return nil }
func (execOnlyDocker) EnsureVolume(string) error { return nil }
func (execOnlyDocker) PullImage(string) error    { return nil }
func (execOnlyDocker) ContainerState(string) (dockerpkg.ContainerState, error) {
	return dockerpkg.StateRunning, nil
}
func (execOnlyDocker) CreateAndStart(dockerpkg.ContainerSpec) error { return nil }
func (execOnlyDocker) Start(string) error                           { return nil }
func (execOnlyDocker) Stop(string) error                            { return nil }
func (execOnlyDocker) Remove(string) error                          { return nil }
func (e execOnlyDocker) Exec(ctx context.Context, _ string, argv []string) ([]byte, error) {
	args := append([]string{"exec", e.container}, argv...)
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

func TestDoctorBackendsFailWhenUpstreamDead(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	adminPort := freePort(t)
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		execOnlyDocker{container: testContainerName})
	if err := a.Register(context.Background(), app.RegisterInput{
		Name:     "deadbackend",
		Upstream: "host.docker.internal:1", // port 1 is unused
		Domain:   "deadbackend.test",
	}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rep, err := a.Doctor(context.Background(), app.DoctorOptions{})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == "backends" && c.Status != app.SeverityFail {
			t.Errorf("backends must FAIL with dead upstream: status=%v summary=%s detail=%v",
				c.Status, c.Summary, c.Detail)
		}
	}
}

func TestDoctorStoppedContainerReportsFail(t *testing.T) {
	SkipUnlessDocker(t)

	// No real container needed - stoppedDocker lies for us.
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient("http://127.0.0.1:1", 100*time.Millisecond),
		stoppedDocker{},
	)

	rep, err := a.Doctor(context.Background(), app.DoctorOptions{SkipBackends: true})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}

	var ch *app.CheckResult
	for i := range rep.Checks {
		if rep.Checks[i].Name == "container-health" {
			ch = &rep.Checks[i]
			break
		}
	}
	if ch == nil {
		t.Fatal("container-health check missing from report")
	}
	if ch.Status != app.SeverityFail {
		t.Errorf("container-health should FAIL when stopped: status=%v summary=%s",
			ch.Status, ch.Summary)
	}
	if ch.Fix == nil {
		t.Error("expected a fix to be offered when container is stopped")
	}
}
