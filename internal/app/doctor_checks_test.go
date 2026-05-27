package app

import (
	"context"
	"testing"

	"github.com/bawdo/caddy_shack/internal/docker"
)

func TestCheckDockerDaemonPass(t *testing.T) {
	fake := docker.NewFake()
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	if r.Fix != nil {
		t.Errorf("docker-daemon has no fix")
	}
}

func TestCheckDockerDaemonFail(t *testing.T) {
	fake := docker.NewFake()
	fake.DaemonErr = docker.ErrDaemonUnreachable
	a := NewWithPaths(Paths{}, nil, fake)
	r := checkDockerDaemon(context.Background(), a)
	if r.Status != SeverityFail {
		t.Errorf("status: %v", r.Status)
	}
	if r.Name != "docker-daemon" {
		t.Errorf("name: %q", r.Name)
	}
}
