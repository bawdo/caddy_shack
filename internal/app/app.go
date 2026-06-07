package app

import (
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
)

// CaddyContainerName is the fixed container name CaddyShack manages.
const CaddyContainerName = "caddy_shack"

// CaddyImage is the pinned Caddy image tag.
const CaddyImage = "caddy:2"

// CaddyDataVolume holds certs and ACME state.
const CaddyDataVolume = "caddy_shack_data"

// CaddyConfigVolume holds Caddy's autosaved config.
const CaddyConfigVolume = "caddy_shack_config"

// CaddyNetworkName is the shared user-defined Docker network that
// container-mode backends are reached over. The caddy container is attached
// to it so it can resolve app containers by their network alias. Host-mode
// (--port) backends never use it.
const CaddyNetworkName = "caddy_shack"

// App is the orchestrator that backs every CLI command.
type App struct {
	paths  Paths
	caddy  *caddyapi.Client
	docker docker.Client
}

// New builds an App with default paths, a real Caddy client at the loopback
// admin port, and the real docker CLI client.
func New() (*App, error) {
	p, err := DefaultPaths()
	if err != nil {
		return nil, err
	}
	return NewWithPaths(p, nil, nil), nil
}

// NewWithPaths is the test-friendly constructor. Passing nil for caddy or
// docker substitutes the production defaults; tests pass their own.
func NewWithPaths(p Paths, caddy *caddyapi.Client, dock docker.Client) *App {
	if caddy == nil {
		caddy = caddyapi.NewClient("http://127.0.0.1:2019", 10*time.Second)
	}
	if dock == nil {
		dock = docker.NewCLI()
	}
	return &App{paths: p, caddy: caddy, docker: dock}
}
