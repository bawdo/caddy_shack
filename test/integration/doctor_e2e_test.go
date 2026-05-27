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

func TestDoctorStoppedContainerReportsFail(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	// Create container then stop it.
	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	adminPort := freePort(t)
	if out, err := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	if out, err := exec.Command("docker", "stop", testContainerName).CombinedOutput(); err != nil {
		t.Fatalf("docker stop: %v: %s", err, out)
	}

	// Use the real CLI docker client so doctor sees the actual stopped state.
	// Override CaddyContainerName via NewWithPaths? No - the real CaddyContainerName
	// is hard-coded. Skip this test path if the test container differs.
	if testContainerName != app.CaddyContainerName {
		t.Skipf("test container name %q != app.CaddyContainerName %q; this test only runs in a CI env where they match",
			testContainerName, app.CaddyContainerName)
	}
	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(app.Paths{StatePath: statePath, LockPath: statePath + ".lock"}, nil, nil)
	rep, err := a.Doctor(context.Background(), app.DoctorOptions{SkipBackends: true})
	if err != nil {
		t.Fatalf("Doctor: %v", err)
	}
	for _, c := range rep.Checks {
		if c.Name == "container-health" && c.Status != app.SeverityFail {
			t.Errorf("container-health should FAIL when stopped: status=%v summary=%s", c.Status, c.Summary)
		}
	}
}
