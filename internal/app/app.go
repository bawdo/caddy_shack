package app

import (
	"fmt"
	"io"
	"os"
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

// Instance names every Docker object and loopback port one CaddyShack
// install owns. Production always uses DefaultInstance; a different
// Instance lets an integration test drive the real container lifecycle
// (install, docker restart, start, upgrade) next to a live install without
// touching its container, volumes, network, or ports.
type Instance struct {
	ContainerName      string
	RelayContainerName string
	NetworkName        string
	DataVolume         string
	ConfigVolume       string
	RelayVolume        string
	LANContainerName   string
	HTTPPort           int
	HTTPSPort          int
	AdminPort          int
	LANHTTPPort        int
	LANHTTPSPort       int
}

// DefaultInstance is the single install the CLI manages.
func DefaultInstance() Instance {
	return Instance{
		ContainerName:      CaddyContainerName,
		RelayContainerName: RelayContainerName,
		NetworkName:        CaddyNetworkName,
		DataVolume:         CaddyDataVolume,
		ConfigVolume:       CaddyConfigVolume,
		RelayVolume:        RelayVolume,
		LANContainerName:   LANContainerName,
		HTTPPort:           80,
		HTTPSPort:          443,
		AdminPort:          2019,
		LANHTTPPort:        80,
		LANHTTPSPort:       443,
	}
}

// AdminURL is the host-side base URL of this instance's Caddy admin API.
func (i Instance) AdminURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", i.AdminPort)
}

// defaultAdminURL picks the admin URL a nil caddy client targets. A var so
// this package's tests can point every default client at a closed port:
// a unit test must never reach (let alone POST /load to) a real install.
var defaultAdminURL = Instance.AdminURL

// App is the orchestrator that backs every CLI command.
type App struct {
	paths  Paths
	inst   Instance
	caddy  *caddyapi.Client
	docker docker.Client
	// notices receives advisory messages (engine fallbacks, recreate
	// announcements, "run install" hints). Defaults to stderr.
	notices io.Writer
	// hn caches the engine classification; see hostNetwork.
	hn *HostNetwork
	// lan finds and checks the LAN address; see LANResolver.
	lan LANResolver
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
	return NewForInstance(p, DefaultInstance(), caddy, dock)
}

// NewForInstance is NewWithPaths for a non-default Instance. A nil caddy
// client targets the instance's own admin port.
func NewForInstance(p Paths, inst Instance, caddy *caddyapi.Client, dock docker.Client) *App {
	if caddy == nil {
		caddy = caddyapi.NewClient(defaultAdminURL(inst), 10*time.Second)
	}
	if dock == nil {
		dock = docker.NewCLI()
	}
	return &App{paths: p, inst: inst, caddy: caddy, docker: dock, notices: os.Stderr, lan: systemLANResolver{}}
}

// notef writes one advisory message to the notices writer.
func (a *App) notef(format string, args ...any) {
	_, _ = fmt.Fprintf(a.notices, format, args...)
}
