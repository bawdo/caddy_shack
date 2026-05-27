//go:build integration

package integration

import (
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// TestCLIContainerStateAbsentForUnknownContainer is a regression test for the
// case-sensitive "No such object" stderr match in cli.go. Modern docker emits
// "no such object" (lowercase), which previously caused ContainerState to
// return a propagated error instead of StateAbsent, breaking the first run
// of `caddy_shack install`.
func TestCLIContainerStateAbsentForUnknownContainer(t *testing.T) {
	SkipUnlessDocker(t)
	c := docker.NewCLI()
	state, err := c.ContainerState("caddy_shack_smoke_does_not_exist")
	if err != nil {
		t.Fatalf("ContainerState(nonexistent): want nil error, got %v", err)
	}
	if state != docker.StateAbsent {
		t.Errorf("ContainerState: got %v want StateAbsent", state)
	}
}
