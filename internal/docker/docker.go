// Package docker is a thin abstraction over the docker CLI for the
// containers CaddyShack manages. The interface keeps app-layer tests off
// real docker; the concrete impl in cli.go shells out for production use.
package docker

import (
	"context"
	"errors"
	"sort"
)

// ContainerState is the lifecycle phase of CaddyShack's Caddy container.
type ContainerState int

const (
	// StateAbsent means no container with the managed name exists.
	StateAbsent ContainerState = iota
	// StateStopped means a container exists but is not running.
	StateStopped
	// StateRunning means a container exists and is running.
	StateRunning
)

func (s ContainerState) String() string {
	switch s {
	case StateAbsent:
		return "absent"
	case StateStopped:
		return "stopped"
	case StateRunning:
		return "running"
	default:
		return "unknown"
	}
}

// ErrDaemonUnreachable indicates the docker CLI could not contact the
// daemon (not installed, not running).
var ErrDaemonUnreachable = errors.New("docker: daemon unreachable")

// Client is the interface the app layer depends on.
type Client interface {
	// DaemonReachable returns nil if `docker info` succeeds.
	DaemonReachable() error
	// EnsureVolume creates the named volume if it does not exist. Idempotent.
	EnsureVolume(name string) error
	// PullImage pulls the image. Idempotent.
	PullImage(image string) error
	// ContainerState reports the lifecycle phase of the named container.
	ContainerState(name string) (ContainerState, error)
	// CreateAndStart creates the container with the given spec and starts it.
	// Errors if a container with the same name already exists.
	CreateAndStart(spec ContainerSpec) error
	// Start starts an existing (stopped) container.
	Start(name string) error
	// Stop stops a running container. No-op if already stopped.
	Stop(name string) error
	// Remove force-removes the named container regardless of its current
	// state. No-op if the container does not exist. Used for rollback when
	// CreateAndStart succeeds partially.
	Remove(name string) error
	// Exec runs a command inside the named running container. Returns the
	// combined stdout/stderr bytes. Returns an error if the container is
	// not running or the command exits non-zero.
	Exec(ctx context.Context, name string, argv []string) ([]byte, error)
	// EnsureNetwork creates the named user-defined bridge network if it
	// does not exist. Idempotent: an already-existing network is success.
	EnsureNetwork(name string) error
	// ConnectNetwork attaches the named container to the named network.
	// Idempotent: an already-attached container is success.
	ConnectNetwork(network, container string) error
	// NetworkAttached reports whether the named container is attached to
	// the named network. Returns (false, nil) if the container is absent.
	NetworkAttached(network, container string) (bool, error)
	// ImageID returns the local image ID (e.g. "sha256:...") for ref.
	// Errors if the image is not present locally.
	ImageID(ref string) (string, error)
	// ContainerImageID returns the image ID the named container was created
	// from. Works against stopped containers.
	ContainerImageID(name string) (string, error)
	// EngineInfo returns the `docker info` fields used to classify the
	// engine (Docker Desktop, a VM runtime, or native Linux).
	EngineInfo() (EngineInfo, error)
	// BridgeGateway returns the IPv4 gateway of the default `bridge`
	// network (the docker0 address on native Linux).
	BridgeGateway() (string, error)
	// InspectContainer returns the creation-time settings of an existing
	// container that `docker run` fixes for its lifetime (network mode,
	// extra hosts, volume mounts) plus the networks it is attached to.
	// Works against stopped containers; errors if the container is absent.
	InspectContainer(name string) (ContainerInfo, error)
}

// EngineInfo is the subset of `docker info` used to classify the engine.
type EngineInfo struct {
	OperatingSystem string
	OSType          string
	Name            string
	Labels          []string
}

// ContainerInfo is the subset of `docker inspect` that decides whether an
// existing container still matches the spec it should have been created
// from. Networks is sorted.
type ContainerInfo struct {
	NetworkMode string
	ExtraHosts  []string
	Mounts      []VolumeMount
	Networks    []string
	// Command is the container's Config.Cmd: the command it was created
	// with (the images CaddyShack runs have no entrypoint).
	Command []string
	// Ports are the published port bindings (HostConfig.PortBindings),
	// sorted by HostAddr, HostPort, ContainerPort.
	Ports []PortMapping
}

// ContainerSpec describes a container CreateAndStart should run.
type ContainerSpec struct {
	Name  string
	Image string
	// NetworkMode is passed as --network when non-empty; empty keeps
	// docker's default bridge.
	NetworkMode string
	// ExtraHosts are passed as --add-host (e.g.
	// "host.docker.internal:host-gateway").
	ExtraHosts   []string
	PortMappings []PortMapping
	Volumes      []VolumeMount
	Command      []string
}

// PortMapping publishes a container port to a specific host address/port.
type PortMapping struct {
	HostAddr      string
	HostPort      int
	ContainerPort int
}

// VolumeMount mounts a named volume at a container path.
type VolumeMount struct {
	VolumeName    string
	ContainerPath string
}

// SortPortMappings orders ports by HostAddr, HostPort, then ContainerPort,
// in place, so two sets can be compared with slices.Equal.
func SortPortMappings(ports []PortMapping) {
	sort.Slice(ports, func(i, j int) bool {
		a, b := ports[i], ports[j]
		if a.HostAddr != b.HostAddr {
			return a.HostAddr < b.HostAddr
		}
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		return a.ContainerPort < b.ContainerPort
	})
}
