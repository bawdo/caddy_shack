package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestRegisterContainerModeSetsContainerDial(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name:      "myapp",
		Container: "myapp",
		Port:      3000,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	s, err := state.Load(f.statePath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := s.Backends["myapp"]
	if b.Upstream != "myapp:3000" {
		t.Errorf("Upstream: got %q want %q", b.Upstream, "myapp:3000")
	}
	if !strings.Contains(string(f.lastBody), "myapp:3000") {
		t.Errorf("pushed config missing container dial; body=%s", f.lastBody)
	}
}

func TestRegisterHostModeUnchanged(t *testing.T) {
	f := newFixture(t, http.StatusOK)

	err := f.app.Register(context.Background(), RegisterInput{
		Name: "myapp",
		Port: 3000,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	s, _ := state.Load(f.statePath)
	if got := s.Backends["myapp"].Upstream; got != "host.docker.internal:3000" {
		t.Errorf("host mode Upstream changed: got %q", got)
	}
}

func TestRegisterContainerInvalidAlias(t *testing.T) {
	f := newFixture(t, http.StatusOK)
	err := f.app.Register(context.Background(), RegisterInput{
		Name:      "myapp",
		Container: "Bad_Alias",
		Port:      3000,
	})
	if !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("expected ErrInvalidArgs, got %v", err)
	}
}
