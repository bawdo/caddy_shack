package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// CLI is the production Client. Shells out to the `docker` binary on PATH.
type CLI struct{}

// NewCLI returns a CLI client. There is no constructor argument; the
// `docker` binary on PATH is used.
func NewCLI() *CLI { return &CLI{} }

func (CLI) DaemonReachable() error {
	cmd := exec.Command("docker", "info")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %v", ErrDaemonUnreachable, err)
	}
	return nil
}

func (CLI) EnsureVolume(name string) error {
	// `docker volume create` is idempotent: succeeds whether or not it exists.
	cmd := exec.Command("docker", "volume", "create", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker volume create %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) PullImage(image string) error {
	cmd := exec.Command("docker", "pull", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker pull %s: %w: %s", image, err, out)
	}
	return nil
}

func (CLI) ContainerState(name string) (ContainerState, error) {
	// --type container: without it docker inspects whatever object has the
	// name, so a volume or network sharing a container's name would be
	// inspected instead (and has no .State).
	cmd := exec.Command("docker", "inspect", "--type", "container", "--format", "{{.State.Running}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// Absent => "No such container" (with --type) or "no such object"
		// (without). exec.ExitError reflects docker's exit 1. Match
		// case-insensitively: docker has used both capitalisations.
		var ee *exec.ExitError
		if errors.As(err, &ee) && isNoSuchContainer(stderr.String()) {
			return StateAbsent, nil
		}
		return StateAbsent, fmt.Errorf("docker inspect %s: %w: %s", name, err, stderr.String())
	}
	switch strings.TrimSpace(stdout.String()) {
	case "true":
		return StateRunning, nil
	case "false":
		return StateStopped, nil
	default:
		return StateAbsent, fmt.Errorf("docker inspect %s: unexpected output %q", name, stdout.String())
	}
}

func (CLI) CreateAndStart(spec ContainerSpec) error {
	cmd := exec.Command("docker", RunArgs(spec)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w: %s", err, out)
	}
	return nil
}

// RunArgs is the `docker run` argv (without the leading "docker") that
// CreateAndStart executes for spec. Pure, so the exact argv each engine
// gets can be pinned in tests. Optional flags are only emitted when set,
// so a spec without NetworkMode/ExtraHosts yields the original argv.
func RunArgs(spec ContainerSpec) []string {
	args := []string{"run", "-d", "--name", spec.Name, "--restart", "unless-stopped"}
	if spec.NetworkMode != "" {
		args = append(args, "--network", spec.NetworkMode)
	}
	for _, h := range spec.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	for _, p := range spec.PortMappings {
		args = append(args, "-p",
			fmt.Sprintf("%s:%s:%s", p.HostAddr, strconv.Itoa(p.HostPort), strconv.Itoa(p.ContainerPort)))
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.VolumeName, v.ContainerPath))
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	return args
}

func (CLI) Start(name string) error {
	cmd := exec.Command("docker", "start", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker start %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) Stop(name string) error {
	cmd := exec.Command("docker", "stop", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// docker stop on a stopped container exits 0, so any error is real.
		return fmt.Errorf("docker stop %s: %w: %s", name, err, out)
	}
	return nil
}

// isNoSuchContainer reports whether docker's stderr says the named
// container does not exist.
func isNoSuchContainer(stderr string) bool {
	low := strings.ToLower(stderr)
	return strings.Contains(low, "no such object") || strings.Contains(low, "no such container")
}

func (CLI) Remove(name string) error {
	cmd := exec.Command("docker", "rm", "-f", name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Absent container is a no-op success.
		if strings.Contains(strings.ToLower(string(out)), "no such object") ||
			strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil
		}
		return fmt.Errorf("docker rm -f %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) Exec(ctx context.Context, name string, argv []string) ([]byte, error) {
	args := append([]string{"exec", name}, argv...)
	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("docker exec %s %v: %w: %s", name, argv, err, out)
	}
	return out, nil
}

func (CLI) EnsureNetwork(name string) error {
	cmd := exec.Command("docker", "network", "create", name)
	if out, err := cmd.CombinedOutput(); err != nil {
		// `docker network create` errors if the network already exists;
		// treat that as success so the call is idempotent.
		if strings.Contains(strings.ToLower(string(out)), "already exists") {
			return nil
		}
		return fmt.Errorf("docker network create %s: %w: %s", name, err, out)
	}
	return nil
}

func (CLI) ConnectNetwork(network, container string) error {
	cmd := exec.Command("docker", "network", "connect", network, container)
	if out, err := cmd.CombinedOutput(); err != nil {
		low := strings.ToLower(string(out))
		// Already attached: docker reports "endpoint with name <c> already
		// exists in network <n>". Treat as success (idempotent).
		if strings.Contains(low, "already exists in network") ||
			strings.Contains(low, "already in network") {
			return nil
		}
		return fmt.Errorf("docker network connect %s %s: %w: %s", network, container, err, out)
	}
	return nil
}

func (CLI) ImageID(ref string) (string, error) {
	cmd := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", ref)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker image inspect %s: %w: %s", ref, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (CLI) ContainerImageID(name string) (string, error) {
	cmd := exec.Command("docker", "inspect", "--type", "container", "--format", "{{.Image}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker inspect %s image: %w: %s", name, err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func (CLI) NetworkAttached(network, container string) (bool, error) {
	cmd := exec.Command("docker", "inspect", "--type", "container", "--format", "{{json .NetworkSettings.Networks}}", container)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && isNoSuchContainer(stderr.String()) {
			return false, nil
		}
		return false, fmt.Errorf("docker inspect %s networks: %w: %s", container, err, stderr.String())
	}
	var nets map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &nets); err != nil {
		return false, fmt.Errorf("parse networks for %s: %w", container, err)
	}
	_, ok := nets[network]
	return ok, nil
}

func (CLI) EngineInfo() (EngineInfo, error) {
	cmd := exec.Command("docker", "info", "--format", "{{json .}}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return EngineInfo{}, fmt.Errorf("docker info: %w: %s", err, stderr.String())
	}
	var info EngineInfo
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &info); err != nil {
		return EngineInfo{}, fmt.Errorf("docker info: parse: %w", err)
	}
	return info, nil
}

func (CLI) BridgeGateway() (string, error) {
	cmd := exec.Command("docker", "network", "inspect", "bridge", "--format", "{{json .IPAM.Config}}")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("docker network inspect bridge: %w: %s", err, stderr.String())
	}
	return parseBridgeGateway(stdout.Bytes())
}

// parseBridgeGateway picks the first IPv4 gateway out of the bridge
// network's IPAM config JSON.
func parseBridgeGateway(out []byte) (string, error) {
	var cfgs []struct{ Gateway string }
	if err := json.Unmarshal(bytes.TrimSpace(out), &cfgs); err != nil {
		return "", fmt.Errorf("docker network inspect bridge: parse: %w", err)
	}
	for _, c := range cfgs {
		if ip := net.ParseIP(c.Gateway); ip != nil && ip.To4() != nil {
			return c.Gateway, nil
		}
	}
	return "", fmt.Errorf("docker network inspect bridge: no IPv4 gateway in %s", strings.TrimSpace(string(out)))
}

func (CLI) InspectContainer(name string) (ContainerInfo, error) {
	cmd := exec.Command("docker", "inspect", "--type", "container", "--format", "{{json .}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return ContainerInfo{}, fmt.Errorf("docker inspect %s: %w: %s", name, err, stderr.String())
	}
	return parseContainerInfo(stdout.Bytes())
}

// parseContainerInfo extracts ContainerInfo from `docker inspect` JSON.
// Only named-volume mounts are reported; bind mounts never appear in a
// CaddyShack spec.
func parseContainerInfo(out []byte) (ContainerInfo, error) {
	var raw struct {
		Config struct {
			Cmd []string
		}
		HostConfig struct {
			NetworkMode string
			ExtraHosts  []string
			// PortBindings is keyed "<container port>/<proto>"; docker
			// reports HostIp and HostPort as strings.
			PortBindings map[string][]struct {
				HostIp   string
				HostPort string
			}
		}
		Mounts []struct {
			Type        string
			Name        string
			Destination string
		}
		NetworkSettings struct {
			Networks map[string]json.RawMessage
		}
	}
	if err := json.Unmarshal(bytes.TrimSpace(out), &raw); err != nil {
		return ContainerInfo{}, fmt.Errorf("parse docker inspect: %w", err)
	}
	info := ContainerInfo{
		NetworkMode: raw.HostConfig.NetworkMode,
		ExtraHosts:  raw.HostConfig.ExtraHosts,
		Command:     raw.Config.Cmd,
	}
	for _, m := range raw.Mounts {
		if m.Type == "volume" {
			info.Mounts = append(info.Mounts, VolumeMount{VolumeName: m.Name, ContainerPath: m.Destination})
		}
	}
	for n := range raw.NetworkSettings.Networks {
		info.Networks = append(info.Networks, n)
	}
	sort.Strings(info.Networks)
	for key, bindings := range raw.HostConfig.PortBindings {
		cport, err := strconv.Atoi(strings.SplitN(key, "/", 2)[0])
		if err != nil {
			continue
		}
		for _, b := range bindings {
			hport, err := strconv.Atoi(b.HostPort)
			if err != nil {
				continue
			}
			info.Ports = append(info.Ports, PortMapping{HostAddr: b.HostIp, HostPort: hport, ContainerPort: cport})
		}
	}
	SortPortMappings(info.Ports)
	return info, nil
}
