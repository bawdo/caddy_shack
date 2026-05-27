package app

import (
	"context"
	"fmt"
	"net"
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

// listenLoopback grabs a free loopback port the test holds open until cleanup.
// Returns the port number for the caller to feed back into the check.
func listenLoopback(t *testing.T) (int, *net.TCPListener) {
	t.Helper()
	addr, _ := net.ResolveTCPAddr("tcp", "127.0.0.1:0")
	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.Addr().(*net.TCPAddr).Port, l
}

func TestCheckPortAvailabilityFailWhenPortHeld(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateAbsent
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityFail {
		t.Errorf("status: %v summary=%s", r.Status, r.Summary)
	}
	want := fmt.Sprintf("%d", heldPort)
	found := false
	for _, d := range r.Detail {
		if contains(d, want) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected detail to mention port %d; got %v", heldPort, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenAllFreeInjected(t *testing.T) {
	// Use injected freePort set so the test does not race against the OS.
	p, l := listenLoopback(t)
	_ = l.Close() // free it for the probe
	a := NewWithPaths(Paths{}, nil, docker.NewFake())

	r := checkPortAvailabilityOn(context.Background(), a, []int{p})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func TestCheckPortAvailabilityPassWhenContainerRunningHoldsPorts(t *testing.T) {
	heldPort, _ := listenLoopback(t)
	fake := docker.NewFake()
	fake.Containers[CaddyContainerName] = docker.StateRunning
	a := NewWithPaths(Paths{}, nil, fake)

	r := checkPortAvailabilityOn(context.Background(), a, []int{heldPort})
	if r.Status != SeverityPass {
		t.Errorf("status: %v summary=%s detail=%v", r.Status, r.Summary, r.Detail)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && (len(haystack) >= len(needle)) && (indexOf(haystack, needle) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
