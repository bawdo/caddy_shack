package caddyapi

import (
	"testing"

	"github.com/bawdo/caddy_shack/internal/state"
)

func TestBuildConfigContainerUpstream(t *testing.T) {
	s := &state.State{
		SchemaVersion: 1,
		Backends: map[string]state.Backend{
			"app": {Domain: "app.test", Upstream: "app:3000"},
		},
	}
	got := Build(s)
	assertJSONMatchesGolden(t, got, "one_container_backend.json")
}
