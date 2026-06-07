//go:build integration

package integration

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/app"
	"github.com/bawdo/caddy_shack/internal/caddyapi"
)

// TestContainerUpstreamE2E proves the container-routing path end-to-end: a
// backend CONTAINER attached to the shared caddy_shack network by a network
// alias is reached by Caddy via <alias>:<port> (NOT host.docker.internal).
//
// Unlike the host-mode tests, the throwaway Caddy container here is attached
// to the shared caddy_shack network at `docker run` time so it can resolve the
// backend by alias. The App is given fakeRunningDocker so its
// ensureSharedNetwork() is a no-op (this test owns the network/container
// lifecycle itself rather than going through App's docker client).
func TestContainerUpstreamE2E(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	// The shared network must exist before either container joins it. Idempotent:
	// `network create` of an existing network errors, which we ignore.
	_ = exec.Command("docker", "network", "create", app.CaddyNetworkName).Run()

	// Unique names/alias so concurrent or repeated runs don't collide.
	suffix := fmt.Sprintf("%d%d", time.Now().UnixNano(), rand.Intn(1000))
	backendName := "csit-backend-" + suffix
	alias := "csitbackend" + suffix
	const backendBody = "hello-from-container"
	const backendPort = 5678

	// Throwaway backend container on the shared network, reachable by alias.
	if out, err := exec.Command("docker", "run", "-d", "--rm",
		"--name", backendName,
		"--network", app.CaddyNetworkName,
		"--network-alias", alias,
		"hashicorp/http-echo",
		"-text="+backendBody,
		"-listen=:"+fmt.Sprintf("%d", backendPort),
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run backend: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", backendName).Run() })

	// Throwaway Caddy container, also on the shared network so it can resolve
	// the backend alias. Same admin/https/http host-port scheme as the other
	// integration tests.
	adminPort := freePort(t)
	httpsPort := freePort(t)
	httpPort := freePort(t)

	caddyfile := filepath.Join(t.TempDir(), "Caddyfile")
	if err := os.WriteFile(caddyfile, []byte("{\n\tadmin 0.0.0.0:2019\n}\n"), 0o644); err != nil {
		t.Fatalf("write Caddyfile: %v", err)
	}

	if out, err := exec.Command("docker", "run", "-d", "--name", testContainerName,
		"--network", app.CaddyNetworkName,
		"--add-host=host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:2019", adminPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:443", httpsPort),
		"-p", fmt.Sprintf("127.0.0.1:%d:80", httpPort),
		"-v", fmt.Sprintf("%s:/etc/caddy/Caddyfile:ro", caddyfile),
		"caddy:2",
	).CombinedOutput(); err != nil {
		t.Fatalf("docker run caddy: %v: %s", err, out)
	}

	adminURL := fmt.Sprintf("http://127.0.0.1:%d", adminPort)
	WaitForCaddyAdmin(t, adminURL, 15*time.Second)

	statePath := filepath.Join(t.TempDir(), "state.json")
	a := app.NewWithPaths(
		app.Paths{StatePath: statePath, LockPath: statePath + ".lock"},
		caddyapi.NewClient(adminURL, 5*time.Second),
		fakeRunningDocker{},
	)

	const name = "csapp"
	if err := a.Register(context.Background(), app.RegisterInput{
		Name:      name,
		Container: alias,
		Port:      backendPort,
	}); err != nil {
		t.Fatalf("Register --container: %v", err)
	}
	t.Cleanup(func() { _, _ = a.Deregister(context.Background(), name, false) })

	// Caddy reverse-proxies https://csapp.test -> <alias>:5678 inside the
	// shared network, returning the backend container's deterministic body.
	// hashicorp/http-echo appends a trailing newline to -text, so the wire
	// body is "<backendBody>\n".
	client := httpsClient(httpsPort)
	assertBody(t, client, "https://"+name+".test/", backendBody+"\n")
}
