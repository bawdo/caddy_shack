package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestInstallCreatesVolumesPullsImageAndStartsContainer(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	if !fake.Volumes[CaddyDataVolume] {
		t.Errorf("data volume not created")
	}
	if !fake.Volumes[CaddyConfigVolume] {
		t.Errorf("config volume not created")
	}
	if !fake.PulledImg[CaddyImage] {
		t.Errorf("image not pulled")
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("container state: got %v want running", state)
	}
	// Spec sanity: published ports include 80/443/2019 on loopback.
	wantPorts := map[int]bool{80: false, 443: false, 2019: false}
	for _, p := range fake.Spec.PortMappings {
		if p.HostAddr != "127.0.0.1" {
			t.Errorf("port %d not bound to loopback: %s", p.HostPort, p.HostAddr)
		}
		if _, ok := wantPorts[p.HostPort]; ok {
			wantPorts[p.HostPort] = true
		}
	}
	for port, seen := range wantPorts {
		if !seen {
			t.Errorf("port %d not published", port)
		}
	}
}

func TestInstallIsNoOpWhenAlreadyRunning(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	// PullImage and CreateAndStart should not have run.
	if fake.PulledImg[CaddyImage] {
		t.Error("image pulled despite already-installed container")
	}
}

func TestInstallStartsStoppedContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("container state: got %v want running", state)
	}
}

func TestInstallReturnsErrDockerUnreachableWhenDaemonDown(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)

	err := a.Install()
	if !errors.Is(err, ErrDockerUnreachable) {
		t.Errorf("expected ErrDockerUnreachable, got %v", err)
	}
}

func TestInstallRollsBackHalfCreatedContainerOnFailure(t *testing.T) {
	fake := docker.NewFake()
	fake.CreateAndStartErr = errors.New("docker run: exit status 125: networking failure")
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Install(); err == nil {
		t.Fatal("expected error from Install")
	}

	// Remove must have been called for the managed container so a retry
	// has a clean slate.
	found := false
	for _, name := range fake.Removed {
		if name == CaddyContainerName {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected Remove(%q) to be called for rollback, got Removed=%v",
			CaddyContainerName, fake.Removed)
	}
	if _, present := fake.Containers[CaddyContainerName]; present {
		t.Errorf("container still in Fake after rollback: %v", fake.Containers)
	}
}

func TestInstallReturnsHelpfulErrorOnPortConflict(t *testing.T) {
	fake := docker.NewFake()
	fake.CreateAndStartErr = errors.New(
		"docker run: exit status 125: Bind for 127.0.0.1:80 failed: port is already allocated")
	a := NewWithPaths(Paths{}, nil, fake)

	err := a.Install()
	if err == nil {
		t.Fatal("expected error from Install")
	}
	msg := err.Error()
	wantSubstrings := []string{"already in use", "docker ps", "80", "443", "2019"}
	for _, w := range wantSubstrings {
		if !strings.Contains(msg, w) {
			t.Errorf("error message missing %q\nfull: %s", w, msg)
		}
	}
}
