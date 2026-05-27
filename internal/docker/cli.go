package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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
	cmd := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// "no such object" => absent. exec.ExitError reflects docker's exit 1.
		// Match case-insensitively: docker has used both "No such object" and
		// "no such object" historically.
		var ee *exec.ExitError
		if errors.As(err, &ee) && strings.Contains(strings.ToLower(stderr.String()), "no such object") {
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
	args := []string{"run", "-d", "--name", spec.Name, "--restart", "unless-stopped"}
	for _, p := range spec.PortMappings {
		args = append(args, "-p",
			fmt.Sprintf("%s:%s:%s", p.HostAddr, strconv.Itoa(p.HostPort), strconv.Itoa(p.ContainerPort)))
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.VolumeName, v.ContainerPath))
	}
	args = append(args, spec.Image)
	args = append(args, spec.Command...)
	cmd := exec.Command("docker", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %w: %s", err, out)
	}
	return nil
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
