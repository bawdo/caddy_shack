package app

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

// newUpgradeFixture extends newFixture with a running container, scripted
// `caddy version` output, and the image-ID maps upgrade inspects.
func newUpgradeFixture(t *testing.T, caddyStatus int) *fixture {
	t.Helper()
	f := newFixture(t, caddyStatus)
	d := f.app.docker.(*docker.Fake)
	d.Containers[CaddyContainerName] = docker.StateRunning
	d.ExecResponses["caddy_shack:caddy version"] = docker.ExecResponse{
		Stdout: []byte("v2.8.4 h1:abcdef="),
	}
	return f
}

func TestUpgradeAlreadyCurrentLeavesContainerUntouched(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:same"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:same"

	rep, err := f.app.Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if !rep.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=true")
	}
	if rep.Version != "v2.8.4" {
		t.Errorf("Version: got %q want v2.8.4", rep.Version)
	}
	if len(d.Removed) != 0 {
		t.Errorf("container should not be removed on no-op: %v", d.Removed)
	}
}

func TestUpgradeRecreatesAndRestoresBackends(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"

	seed := &state.State{
		SchemaVersion: state.SchemaVersion,
		Backends: map[string]state.Backend{
			"myapp": {Domain: "myapp.test", Upstream: "host.docker.internal:3000", RegisteredAt: "2026-06-22T00:00:00Z"},
		},
	}
	if err := state.Save(f.statePath, seed); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	rep, err := f.app.Upgrade(context.Background())
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if rep.AlreadyCurrent {
		t.Error("expected AlreadyCurrent=false")
	}
	if rep.Version != "v2.8.4" {
		t.Errorf("Version: got %q want v2.8.4", rep.Version)
	}
	if !d.PulledImg[CaddyImage] {
		t.Error("image was not pulled")
	}
	if len(d.Removed) != 1 || d.Removed[0] != CaddyContainerName {
		t.Errorf("Removed: got %v want [caddy_shack]", d.Removed)
	}
	if d.Spec.Image != CaddyImage {
		t.Errorf("recreated with image %q want %q", d.Spec.Image, CaddyImage)
	}
	if !d.Volumes[CaddyDataVolume] || !d.Volumes[CaddyConfigVolume] {
		t.Error("both volumes should be ensured")
	}
	if !d.NetworkConnections[CaddyNetworkName+":"+CaddyContainerName] {
		t.Error("shared network not reattached")
	}
	if !strings.Contains(string(f.lastBody), "myapp.test") {
		t.Errorf("backends not restored to Caddy: %s", f.lastBody)
	}
}

func TestUpgradeNotInstalledReturnsErrNotInstalled(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	delete(d.Containers, CaddyContainerName)

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestUpgradeDaemonUnreachableReturnsErrDockerUnreachable(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.DaemonErr = errors.New("daemon down")

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
}

func TestUpgradeCorruptStateDoesNotTouchContainer(t *testing.T) {
	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
	if err := os.WriteFile(f.statePath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	_, err := f.app.Upgrade(context.Background())
	if !errors.Is(err, ErrStateCorrupt) {
		t.Errorf("expected ErrStateCorrupt, got %v", err)
	}
	if len(d.Removed) != 0 {
		t.Errorf("container mutated despite corrupt state: %v", d.Removed)
	}
}

func TestUpgradeReloadFailureReturnsGenericError(t *testing.T) {
	savedTimeout, savedInterval := adminReadyTimeout, adminReadyInterval
	adminReadyTimeout, adminReadyInterval = 50*time.Millisecond, 10*time.Millisecond
	defer func() { adminReadyTimeout, adminReadyInterval = savedTimeout, savedInterval }()

	f := newUpgradeFixture(t, http.StatusOK)
	d := f.app.docker.(*docker.Fake)
	d.ImageIDs[CaddyImage] = "sha256:new"
	d.ContainerImageIDs[CaddyContainerName] = "sha256:old"
	f.caddySrv.Close() // admin API never comes up after the recreate

	_, err := f.app.Upgrade(context.Background())
	if err == nil {
		t.Fatal("expected an error when the admin API never becomes reachable")
	}
	if errors.Is(err, ErrNotInstalled) || errors.Is(err, ErrDockerUnreachable) ||
		errors.Is(err, ErrStateCorrupt) || errors.Is(err, ErrLockContention) {
		t.Errorf("expected a generic error, got a sentinel: %v", err)
	}
}
