package app

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// shellExecutor runs an external command and returns combined output. Only
// checks that shell out to system tools (dscacheutil, security, brew,
// sudo) take this parameter; tests substitute a fake.
type shellExecutor func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultShell is the production executor. CommandContext lets ctx cancel
// the child if Doctor is cancelled.
func defaultShell(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func checkDockerDaemon(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "docker-daemon",
			Status:  SeverityFail,
			Summary: "Docker daemon unreachable",
			Detail:  []string{err.Error(), "start Docker Desktop and re-run"},
		}
	}
	return CheckResult{
		Name:    "docker-daemon",
		Status:  SeverityPass,
		Summary: "Docker reachable",
	}
}

func checkContainerHealth(ctx context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeveritySkip,
			Summary: "docker daemon unreachable",
		}
	}
	state, err := a.docker.ContainerState(CaddyContainerName)
	if err != nil {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "cannot inspect container",
			Detail:  []string{err.Error()},
		}
	}
	switch state {
	case docker.StateAbsent:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container not installed",
			Fix: &FixAction{
				Label: "install Caddy container ('caddy_shack install')",
				Apply: func(_ context.Context) error { return a.Install() },
			},
		}
	case docker.StateStopped:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container stopped",
			Fix: &FixAction{
				Label: "start Caddy container ('caddy_shack start')",
				Apply: func(_ context.Context) error { return a.Start() },
			},
		}
	}
	if !a.caddy.Reachable(ctx) {
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "container running but admin API unreachable",
			Detail:  []string{"check 'docker logs caddy_shack' for Caddy startup errors"},
			Fix: &FixAction{
				Label: "restart container ('caddy_shack restart')",
				Apply: func(_ context.Context) error { return a.Restart() },
			},
		}
	}
	return CheckResult{
		Name:    "container-health",
		Status:  SeverityPass,
		Summary: "running; admin API responding",
	}
}

func randomTestSubdomain() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b) + ".test"
}

// dnsmasqConfPaths returns the candidate brew dnsmasq.d directories. Asks
// brew --prefix first and uses that if it succeeds; otherwise falls back
// to the two well-known macOS install locations.
func dnsmasqConfPaths(ctx context.Context, sh shellExecutor) []string {
	if out, err := sh(ctx, "brew", "--prefix"); err == nil {
		prefix := strings.TrimSpace(string(out))
		if prefix != "" {
			return []string{filepath.Join(prefix, "etc", "dnsmasq.d")}
		}
	}
	return []string{"/opt/homebrew/etc/dnsmasq.d", "/usr/local/etc/dnsmasq.d"}
}

func checkDNS(ctx context.Context, _ *App, sh shellExecutor) CheckResult {
	host := randomTestSubdomain()
	out, _ := sh(ctx, "dscacheutil", "-q", "host", "-a", "name", host)
	if strings.Contains(string(out), "127.0.0.1") {
		return CheckResult{
			Name:    "dns-test-tld",
			Status:  SeverityPass,
			Summary: ".test resolves to 127.0.0.1",
		}
	}

	// Diagnostic sub-probes.
	var detail []string
	if _, err := sh(ctx, "brew", "list", "dnsmasq"); err != nil {
		detail = append(detail, "dnsmasq: not installed via brew")
	} else {
		detail = append(detail, "dnsmasq: installed")
	}
	foundConf := false
	for _, dir := range dnsmasqConfPaths(ctx, sh) {
		if _, err := sh(ctx, "test", "-f", filepath.Join(dir, "caddy_shack.conf")); err == nil {
			foundConf = true
			break
		}
	}
	if !foundConf {
		detail = append(detail, "dnsmasq.d/caddy_shack.conf: missing")
	}
	if _, err := sh(ctx, "test", "-f", "/etc/resolver/test"); err != nil {
		detail = append(detail, "/etc/resolver/test: missing")
	}

	fix := &FixAction{
		Label:        "install dnsmasq + write /etc/resolver/test (requires sudo)",
		RequiresSudo: true,
		Apply: func(ctx context.Context) error {
			return applyDNSFix(ctx, sh)
		},
	}
	return CheckResult{
		Name:    "dns-test-tld",
		Status:  SeverityFail,
		Summary: ".test does not resolve to 127.0.0.1",
		Detail:  detail,
		Fix:     fix,
	}
}

// applyDNSFix runs the brew + sudo commands documented in the README's
// "local DNS for .test" section. Returns on the first error.
func applyDNSFix(ctx context.Context, sh shellExecutor) error {
	steps := [][]string{
		{"brew", "install", "dnsmasq"},
		{"sh", "-c", `mkdir -p "$(brew --prefix)/etc/dnsmasq.d" && echo 'address=/.test/127.0.0.1' > "$(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf"`},
		{"sudo", "brew", "services", "restart", "dnsmasq"},
		{"sudo", "mkdir", "-p", "/etc/resolver"},
		{"sudo", "sh", "-c", "echo 'nameserver 127.0.0.1' > /etc/resolver/test"},
	}
	for _, s := range steps {
		out, err := sh(ctx, s[0], s[1:]...)
		if err != nil {
			return fmt.Errorf("%s %s: %w: %s", s[0], strings.Join(s[1:], " "), err, out)
		}
	}
	return nil
}

// checkBackends is implemented in Task 13.
func checkBackends(_ context.Context, _ *App) CheckResult {
	return CheckResult{Name: "backends", Status: SeveritySkip, Summary: "not implemented"}
}

// doctorPorts is the list of loopback ports CaddyShack publishes from the
// container. Kept package-private so the check has a single source of truth.
var doctorPorts = []int{80, 443, 2019}

func checkPortAvailability(ctx context.Context, a *App) CheckResult {
	return checkPortAvailabilityOn(ctx, a, doctorPorts)
}

// checkPortAvailabilityOn is the test seam. Tests call it with a free
// ephemeral port so they don't need root for 80/443.
func checkPortAvailabilityOn(_ context.Context, a *App, ports []int) CheckResult {
	var taken []string
	for _, p := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(p))
		if err != nil {
			taken = append(taken, fmt.Sprintf("port %d held: %v", p, err))
			continue
		}
		_ = l.Close()
	}
	if len(taken) == 0 {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "80, 443, 2019 free or held by caddy_shack",
		}
	}
	// If our container is running, assume it holds the ports (the spec's
	// "free or already held by caddy_shack" wording). PASS with a note.
	state, _ := a.docker.ContainerState(CaddyContainerName)
	if state == docker.StateRunning {
		return CheckResult{
			Name:    "port-availability",
			Status:  SeverityPass,
			Summary: "ports held by caddy_shack (container running)",
		}
	}
	return CheckResult{
		Name:    "port-availability",
		Status:  SeverityFail,
		Summary: "required loopback ports are in use by a foreign process",
		Detail:  append(taken, "stop the process holding the port, or run 'caddy_shack stop' if you have another caddy_shack install"),
	}
}
