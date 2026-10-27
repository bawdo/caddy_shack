package app

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

func netDoctorApp(t *testing.T, fake *docker.Fake, backends map[string]state.Backend) *App {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "state.json")
	if err := state.Save(sp, &state.State{SchemaVersion: 1, Backends: backends}); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return NewWithPaths(Paths{StatePath: sp, LockPath: sp + ".lock"}, nil, fake)
}

func TestSharedNetworkPassesWithNoContainerBackends(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"web": {Domain: "web.test", Upstream: "host.docker.internal:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityPass {
		t.Fatalf("status: got %v want pass (%s)", r.Status, r.Summary)
	}
}

func TestSharedNetworkFailsWhenContainerBackendButNotAttached(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"app": {Domain: "app.test", Upstream: "app:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityFail {
		t.Fatalf("status: got %v want fail", r.Status)
	}
	if r.Fix == nil {
		t.Fatal("expected a fix action")
	}
	if _, err := a.ApplyFix(context.Background(), r, func(string) bool { return true }); err != nil {
		t.Fatalf("ApplyFix: %v", err)
	}
	info, _ := fake.InspectContainer(CaddyContainerName)
	attached := slices.Contains(info.Networks, CaddyNetworkName)
	if !attached {
		t.Error("fix did not attach caddy to shared network")
	}
}

func TestSharedNetworkPassesWhenAttached(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	_ = fake.ConnectNetwork(CaddyNetworkName, CaddyContainerName)
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"app": {Domain: "app.test", Upstream: "app:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityPass {
		t.Fatalf("status: got %v want pass (%s)", r.Status, r.Summary)
	}
}

func TestSharedNetworkSkippedWhenContainerNotRunning(t *testing.T) {
	fake := docker.NewFake() // absent
	a := netDoctorApp(t, fake, map[string]state.Backend{})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeveritySkip {
		t.Fatalf("status: got %v want skip", r.Status)
	}
}

func TestSharedNetworkWarnsWhenInspectFails(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	fake.InspectErr = errors.New("inspect boom")
	a := netDoctorApp(t, fake, map[string]state.Backend{
		"app": {Domain: "app.test", Upstream: "app:3000"},
	})
	r := checkSharedNetwork(context.Background(), a)
	if r.Status != SeverityWarn {
		t.Fatalf("status: got %v want warn (%s)", r.Status, r.Summary)
	}
	if len(r.Detail) == 0 {
		t.Error("expected non-empty Detail describing the inspect failure")
	}
}
