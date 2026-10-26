package app

import (
	"slices"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestEnsureSharedNetworkCreatesAndConnects(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.ensureSharedNetwork(); err != nil {
		t.Fatalf("ensureSharedNetwork: %v", err)
	}
	if !fake.Networks[CaddyNetworkName] {
		t.Errorf("network %q not created", CaddyNetworkName)
	}
	info, _ := fake.InspectContainer(CaddyContainerName)
	attached := slices.Contains(info.Networks, CaddyNetworkName)
	if !attached {
		t.Errorf("caddy not attached to %q", CaddyNetworkName)
	}
}
