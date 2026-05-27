package app

import (
	"errors"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestStartReturnsErrNotInstalledWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	err := a.Start()
	if !errors.Is(err, ErrNotInstalled) {
		t.Errorf("expected ErrNotInstalled, got %v", err)
	}
}

func TestStartStartsStoppedContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateStopped
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("state: %v", state)
	}
}

func TestStartIsNoOpWhenRunning(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestStopIsNoOpWhenAbsent(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Errorf("expected nil err on absent stop, got %v", err)
	}
}

func TestStopStopsRunningContainer(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateStopped {
		t.Errorf("state: %v", state)
	}
}

func TestRestartStopsThenStarts(t *testing.T) {
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	if err := a.Restart(); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	state, _ := fake.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		t.Errorf("state: %v", state)
	}
}
