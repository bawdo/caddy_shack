package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
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

func checkTrustStore(ctx context.Context, a *App, sh shellExecutor) CheckResult {
	state, _ := a.docker.ContainerState(CaddyContainerName)
	if state != docker.StateRunning {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	cert, err := a.docker.Exec(ctx, CaddyContainerName, []string{"cat", "/data/caddy/pki/authorities/local/root.crt"})
	if err != nil {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityWarn,
			Summary: "no root CA yet (register a backend to trigger provisioning)",
			Detail:  []string{err.Error()},
		}
	}
	sum := sha256.Sum256(cert)
	wantSHA := strings.ToUpper(hex.EncodeToString(sum[:]))

	dump, err := sh(ctx, "security", "find-certificate", "-a", "-Z", "/Library/Keychains/System.keychain")
	if err == nil && strings.Contains(string(dump), wantSHA) {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityPass,
			Summary: "Caddy root CA trusted in System keychain",
		}
	}
	return CheckResult{
		Name:    "trust-store",
		Status:  SeverityFail,
		Summary: "Caddy root CA not trusted in System keychain",
		Detail:  []string{"expected SHA-256: " + wantSHA},
		Fix: &FixAction{
			Label:        "trust Caddy root CA via 'security add-trusted-cert' (requires sudo)",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyTrustStoreFix(ctx, sh, cert)
			},
		},
	}
}

func applyTrustStoreFix(ctx context.Context, sh shellExecutor, cert []byte) error {
	// Write cert to a temp file the user's sudo can read.
	tmp, err := os.CreateTemp("", "caddy_shack_root_*.crt")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if _, err := tmp.Write(cert); err != nil {
		return fmt.Errorf("write cert: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	out, err := sh(ctx, "sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", tmp.Name())
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %w: %s", err, out)
	}
	return nil
}

func checkStateIntegrity(_ context.Context, a *App) CheckResult {
	if _, err := state.Load(a.paths.StatePath); err != nil {
		if state.IsCorrupt(err) {
			return CheckResult{
				Name:    "state-integrity",
				Status:  SeverityFail,
				Summary: "state.json is corrupt",
				Detail:  []string{err.Error(), "remove " + a.paths.StatePath + " to start fresh (you will lose registered backends)"},
			}
		}
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityFail,
			Summary: "cannot read state.json",
			Detail:  []string{err.Error()},
		}
	}
	held, err := state.LockHeld(a.paths.LockPath)
	if err != nil {
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityWarn,
			Summary: "cannot probe state lock",
			Detail:  []string{err.Error()},
		}
	}
	if held {
		return CheckResult{
			Name:    "state-integrity",
			Status:  SeverityWarn,
			Summary: "register/deregister in progress (state lock held)",
		}
	}
	return CheckResult{
		Name:    "state-integrity",
		Status:  SeverityPass,
		Summary: "state.json parses; lock free",
	}
}

func checkBackends(ctx context.Context, a *App) CheckResult {
	cstate, _ := a.docker.ContainerState(CaddyContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{
			Name:    "backends",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{
			Name:    "backends",
			Status:  SeveritySkip,
			Summary: "state unreadable; covered by state-integrity",
		}
	}
	if len(st.Backends) == 0 {
		return CheckResult{
			Name:    "backends",
			Status:  SeverityPass,
			Summary: "no backends registered",
		}
	}
	var failed []string
	names := make([]string, 0, len(st.Backends))
	for n := range st.Backends {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		b := st.Backends[n]
		host, port, err := net.SplitHostPort(b.Upstream)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s (%s): malformed upstream: %v", n, b.Upstream, err))
			continue
		}
		if probeBackend(ctx, a, host, port) {
			continue
		}
		failed = append(failed, fmt.Sprintf("%s -> %s: unreachable", n, b.Upstream))
	}
	if len(failed) > 0 {
		return CheckResult{
			Name:    "backends",
			Status:  SeverityFail,
			Summary: fmt.Sprintf("%d of %d backends unreachable", len(failed), len(st.Backends)),
			Detail:  failed,
		}
	}
	return CheckResult{
		Name:    "backends",
		Status:  SeverityPass,
		Summary: fmt.Sprintf("all %d backends reachable", len(st.Backends)),
	}
}

// probeBackend returns true if the upstream is reachable from inside the
// container. Tries `nc -z` first; on any error, falls back to wget --spider.
func probeBackend(ctx context.Context, a *App, host, port string) bool {
	if _, err := a.docker.Exec(ctx, CaddyContainerName,
		[]string{"nc", "-z", "-w", "3", host, port}); err == nil {
		return true
	}
	url := "http://" + net.JoinHostPort(host, port) + "/"
	_, err := a.docker.Exec(ctx, CaddyContainerName,
		[]string{"wget", "-q", "--spider", "--tries", "1", "--timeout", "3", url})
	return err == nil
}

// doctorPorts is the list of loopback ports CaddyShack publishes from the
// container. Kept package-private so the check has a single source of truth.
var doctorPorts = []int{80, 443, 2019}

func checkPortAvailability(ctx context.Context, a *App) CheckResult {
	return checkPortAvailabilityOn(ctx, a, doctorPorts)
}

// checkPortAvailabilityOn is the test seam. Tests call it with a free
// ephemeral port so they don't need root for 80/443.
// Uses DialTimeout rather than net.Listen to avoid EACCES on ports 80/443
// when running as a non-root user.
func checkPortAvailabilityOn(_ context.Context, a *App, ports []int) CheckResult {
	var taken []string
	for _, p := range ports {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(p), 300*time.Millisecond)
		if err == nil {
			// Dial succeeded: something is listening on this port.
			_ = conn.Close()
			taken = append(taken, fmt.Sprintf("port %d is in use (something is listening)", p))
		}
		// Dial failed (connection refused or timeout) means port is free.
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

// extractRoutes pulls apps.http.servers.srv0.routes out of a Caddy config
// blob. Returns nil if any expected key is missing (treated as drift).
func extractRoutes(cfg []byte) any {
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		return nil
	}
	step := func(parent any, key string) any {
		mp, ok := parent.(map[string]any)
		if !ok {
			return nil
		}
		return mp[key]
	}
	apps := step(m, "apps")
	http := step(apps, "http")
	servers := step(http, "servers")
	srv0 := step(servers, "srv0")
	if mp, ok := srv0.(map[string]any); ok {
		return mp["routes"]
	}
	return nil
}

func checkConfigDrift(ctx context.Context, a *App) CheckResult {
	cstate, _ := a.docker.ContainerState(CaddyContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	live, err := a.caddy.GetConfig(ctx)
	if err != nil {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityFail,
			Summary: "cannot fetch live config",
			Detail:  []string{err.Error()},
		}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeveritySkip,
			Summary: "state unreadable; covered by state-integrity",
		}
	}
	desired := caddyapi.Build(st)
	liveRoutes := extractRoutes(live)
	desiredRoutes := extractRoutes(desired)

	if reflect.DeepEqual(liveRoutes, desiredRoutes) {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityPass,
			Summary: "live Caddy routes match state.json",
		}
	}
	return CheckResult{
		Name:    "config-drift",
		Status:  SeverityWarn,
		Summary: "live Caddy routes differ from state.json",
		Detail:  []string{"re-POST /load from state will overwrite live config"},
		Fix: &FixAction{
			Label: "re-POST /load from state.json (takes the state lock)",
			Apply: func(ctx context.Context) error {
				return applyDriftFix(ctx, a)
			},
		},
	}
}

func applyDriftFix(ctx context.Context, a *App) error {
	lock, err := state.AcquireLock(a.paths.LockPath, 5*time.Second)
	if err != nil {
		if errors.Is(err, state.ErrLockTimeout) {
			return fmt.Errorf("%w: %v", ErrLockContention, err)
		}
		return err
	}
	defer func() { _ = lock.Release() }()

	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return err
	}
	cfg := caddyapi.Build(st)
	if err := a.caddy.Load(ctx, cfg); err != nil {
		return err
	}
	return nil
}
