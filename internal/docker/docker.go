// Package docker is a thin abstraction over the docker CLI for the single
// container CaddyShack manages. The interface keeps app-layer tests off
// real docker; the concrete impl in cli.go shells out for production use.
package docker

import "errors"

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
}

// ContainerSpec describes the CaddyShack container CreateAndStart should run.
type ContainerSpec struct {
	Name         string
	Image        string
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
