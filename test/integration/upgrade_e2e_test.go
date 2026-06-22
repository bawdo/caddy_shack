//go:build integration

package integration

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// TestUpgradeDockerPrimitivesE2E exercises the docker-layer calls that
// App.Upgrade relies on against a real daemon: image-ID inspection (used to
// decide whether a recreate is needed) and `caddy version` via exec (used to
// report the resulting version).
//
// App.Upgrade itself is hardwired to the production container name
// (caddy_shack) and host ports 80/443/2019, so driving it directly here would
// clobber a developer's real install. These primitives are the parts the
// app-layer unit tests had to fake, so this test closes that gap.
func TestUpgradeDockerPrimitivesE2E(t *testing.T) {
	SkipUnlessDocker(t)
	CleanupContainer(t)

	c := docker.NewCLI()

	if err := c.PullImage("caddy:2"); err != nil {
		t.Fatalf("PullImage caddy:2: %v", err)
	}

	// Start a throwaway caddy:2 container. No host ports are published, so it
	// never clashes with a real install or anything on 80/443/2019.
	run := exec.Command("docker", "run", "-d", "--name", testContainerName, "caddy:2")
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}

	// The container was created from caddy:2, so the image it runs must equal
	// the local caddy:2 image ID. This is exactly the comparison Upgrade uses
	// to detect "already up to date".
	imgID, err := c.ImageID("caddy:2")
	if err != nil {
		t.Fatalf("ImageID: %v", err)
	}
	if !strings.HasPrefix(imgID, "sha256:") {
		t.Errorf("ImageID: got %q, want a sha256: digest", imgID)
	}
	ctrID, err := c.ContainerImageID(testContainerName)
	if err != nil {
		t.Fatalf("ContainerImageID: %v", err)
	}
	if ctrID != imgID {
		t.Errorf("container image %q != image %q; the no-op equality check would misfire", ctrID, imgID)
	}

	// `caddy version` via exec is how Upgrade reports the running version.
	// Confirm the real output leads with a v-prefixed version, which
	// parseCaddyVersion (unit-tested separately) takes as its first field.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := c.Exec(ctx, testContainerName, []string{"caddy", "version"})
	if err != nil {
		t.Fatalf("Exec caddy version: %v", err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "v") {
		t.Errorf("caddy version output %q; want first field like vX.Y.Z", out)
	}
}
