package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func newNetFixtureApp(t *testing.T, fake *docker.Fake) *App {
	t.Helper()
	dir := t.TempDir()
	return NewWithPaths(Paths{
		StatePath: dir + "/state.json",
		LockPath:  dir + "/state.json.lock",
	}, nil, fake)
}

func TestInstallAttachesSharedNetwork(t *testing.T) {
	fake := docker.NewFake() // container absent -> create path
	a := newNetFixtureApp(t, fake)

	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !fake.Networks[CaddyNetworkName] {
		t.Errorf("network %q not created on install", CaddyNetworkName)
	}
	attached, _ := fake.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Errorf("caddy not attached to %q on install", CaddyNetworkName)
	}
}

func TestStartAttachesSharedNetwork(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateStopped

	if err := f.app.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	attached, _ := d.NetworkAttached(CaddyNetworkName, CaddyContainerName)
	if !attached {
		t.Errorf("caddy not attached to %q on start", CaddyNetworkName)
	}
}
