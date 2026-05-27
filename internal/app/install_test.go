package app

import (
	"errors"
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
