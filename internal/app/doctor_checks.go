package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

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

// defaultTTYShell runs a command with stdin/stdout/stderr inherited from
// the parent process. Used for sudo-requiring fixes so the user can type
// their password into the macOS sudo prompt. Returns nil output (output
// went straight to the terminal); err is the command's exit status.
func defaultTTYShell(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil, nil
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
	state, err := a.docker.ContainerState(a.inst.ContainerName)
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
				Apply: func(ctx context.Context) error { return a.Install(ctx) },
			},
		}
	case docker.StateRestarting:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container is restarting (crash-looping)",
			Detail:  []string{"check 'docker logs " + a.inst.ContainerName + "' for the reason it keeps exiting"},
			Fix: &FixAction{
				Label: "restart container ('caddy_shack restart')",
				Apply: func(ctx context.Context) error { return a.Restart(ctx) },
			},
		}
	case docker.StateStopped:
		return CheckResult{
			Name:    "container-health",
			Status:  SeverityFail,
			Summary: "caddy_shack container stopped",
			Fix: &FixAction{
				Label: "start Caddy container ('caddy_shack start')",
				Apply: func(ctx context.Context) error { return a.Start(ctx) },
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
				Apply: func(ctx context.Context) error { return a.Restart(ctx) },
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

func checkDNSDarwin(ctx context.Context, _ *App, sh shellExecutor, tty shellExecutor) CheckResult {
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
	detail = append(detail,
		"to fix manually:",
		"  brew install dnsmasq",
		"  echo 'address=/.test/127.0.0.1' > $(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf",
		"  sudo brew services restart dnsmasq",
		"  sudo mkdir -p /etc/resolver",
		"  echo 'nameserver 127.0.0.1' | sudo tee /etc/resolver/test",
	)

	fix := &FixAction{
		Label:        "install dnsmasq and write resolver entry for .test",
		RequiresSudo: true,
		Apply: func(ctx context.Context) error {
			return applyDNSFixDarwin(ctx, sh, tty)
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

// applyDNSFixDarwin runs the brew + sudo commands documented in the
// README's "local DNS for .test" section. Returns on the first error.
// Non-sudo steps use sh; sudo steps use tty so the password prompt reaches
// the user's terminal.
func applyDNSFixDarwin(ctx context.Context, sh, tty shellExecutor) error {
	type step struct {
		cmd  []string
		sudo bool
	}
	steps := []step{
		{cmd: []string{"brew", "install", "dnsmasq"}},
		{cmd: []string{"sh", "-c", `mkdir -p "$(brew --prefix)/etc/dnsmasq.d" && echo 'address=/.test/127.0.0.1' > "$(brew --prefix)/etc/dnsmasq.d/caddy_shack.conf"`}},
		{cmd: []string{"sudo", "brew", "services", "restart", "dnsmasq"}, sudo: true},
		{cmd: []string{"sudo", "mkdir", "-p", "/etc/resolver"}, sudo: true},
		{cmd: []string{"sudo", "sh", "-c", "echo 'nameserver 127.0.0.1' > /etc/resolver/test"}, sudo: true},
	}
	for _, s := range steps {
		exec := sh
		if s.sudo {
			exec = tty
		}
		out, err := exec(ctx, s.cmd[0], s.cmd[1:]...)
		if err != nil {
			return fmt.Errorf("%s %s: %w: %s", s.cmd[0], strings.Join(s.cmd[1:], " "), err, out)
		}
	}
	return nil
}

func checkTrustStoreDarwin(ctx context.Context, a *App, sh shellExecutor, tty shellExecutor) CheckResult {
	state, _ := a.docker.ContainerState(a.inst.ContainerName)
	if state != docker.StateRunning {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	cert, err := a.docker.Exec(ctx, a.inst.ContainerName, []string{"cat", "/data/caddy/pki/authorities/local/root.crt"})
	if err != nil {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityWarn,
			Summary: "no root CA yet (register a backend to trigger provisioning)",
			Detail:  []string{err.Error()},
		}
	}
	// security find-certificate -Z reports the SHA-256 of the DER-encoded
	// certificate, not the SHA of whatever file format we read. Decode the
	// PEM block first; fall back to hashing the raw bytes if it isn't PEM
	// (Caddy currently writes PEM but the defensive path keeps the check
	// producing a fingerprint either way).
	derBytes := cert
	if block, _ := pem.Decode(cert); block != nil {
		derBytes = block.Bytes
	}
	sum := sha256.Sum256(derBytes)
	wantSHA := strings.ToUpper(hex.EncodeToString(sum[:]))

	dump, err := sh(ctx, "security", "find-certificate", "-a", "-Z", "/Library/Keychains/System.keychain")
	if err == nil && strings.Contains(string(dump), wantSHA) {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityPass,
			Summary: "Caddy root CA trusted in System keychain",
		}
	}
	detail := []string{}
	if err != nil {
		detail = append(detail, "security find-certificate failed: "+err.Error())
	} else {
		detail = append(detail, "expected SHA-256 not present in System keychain dump")
	}
	detail = append(detail,
		"expected SHA-256 (DER): "+wantSHA,
		"cert source: "+a.inst.ContainerName+":/data/caddy/pki/authorities/local/root.crt",
		"to fix manually:",
		"  docker exec "+a.inst.ContainerName+" cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_shack_root.crt",
		"  sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain /tmp/caddy_shack_root.crt",
	)
	return CheckResult{
		Name:    "trust-store",
		Status:  SeverityFail,
		Summary: "Caddy root CA not trusted in System keychain",
		Detail:  detail,
		Fix: &FixAction{
			Label:        "trust Caddy root CA in System keychain",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyTrustStoreFixDarwin(ctx, tty, cert)
			},
		},
	}
}

func applyTrustStoreFixDarwin(ctx context.Context, tty shellExecutor, cert []byte) error {
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
	out, err := tty(ctx, "sudo", "security", "add-trusted-cert", "-d", "-r", "trustRoot",
		"-k", "/Library/Keychains/System.keychain", tmp.Name())
	if err != nil {
		return fmt.Errorf("security add-trusted-cert: %w: %s", err, out)
	}
	return nil
}

// Linux paths for the dns-test-tld and trust-store checks. Kept as package
// vars (not consts) so tests can point them at a scratch directory instead
// of the real /etc.
var (
	linuxDNSMasqCaddyConfPath = "/etc/dnsmasq.d/caddy_shack.conf"
	linuxResolvedDropinDir    = "/etc/systemd/resolved.conf.d"
	linuxResolvedDropinPath   = "/etc/systemd/resolved.conf.d/30-caddy-shack-test-tld.conf"
)

// linuxCABundlePaths are the well-known locations Linux distros write their
// extracted system CA bundle to. Checked in order; the first that exists is
// used. Covers Arch/Omarchy and Debian/Ubuntu (ca-certificates.crt) and
// Fedora/RHEL (ca-bundle.crt / tls-ca-bundle.pem).
var linuxCABundlePaths = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/ca-bundle.pem",
}

// checkDNSLinux is the Linux counterpart of checkDNSDarwin. Linux DNS
// stacks vary a lot more than macOS's (systemd-resolved, NetworkManager +
// dnsmasq, plain resolv.conf), so the resolution probe itself goes through
// getent (the same NSS/getaddrinfo(3) path curl, browsers, and most
// language runtimes use) rather than a stack-specific tool. The diagnostic
// sub-probes and the automatic fix are systemd-resolved-specific (Omarchy
// and most modern distros default to it); on other stacks the fix declines
// and points at the README's manual steps instead of guessing.
func checkDNSLinux(ctx context.Context, _ *App, sh shellExecutor, tty shellExecutor) CheckResult {
	host := randomTestSubdomain()
	out, _ := sh(ctx, "getent", "ahosts", host)
	if strings.Contains(string(out), "127.0.0.1") {
		return CheckResult{
			Name:    "dns-test-tld",
			Status:  SeverityPass,
			Summary: ".test resolves to 127.0.0.1",
		}
	}

	var detail []string
	if _, err := sh(ctx, "sh", "-c", "command -v dnsmasq"); err != nil {
		detail = append(detail, "dnsmasq: not installed")
	} else {
		detail = append(detail, "dnsmasq: installed")
		if out, err := sh(ctx, "systemctl", "is-active", "dnsmasq"); err != nil || !strings.Contains(string(out), "active") {
			detail = append(detail, "dnsmasq.service: not active")
		} else {
			detail = append(detail, "dnsmasq.service: active")
		}
	}
	if _, err := sh(ctx, "test", "-f", linuxDNSMasqCaddyConfPath); err != nil {
		detail = append(detail, linuxDNSMasqCaddyConfPath+": missing")
	}
	if _, err := sh(ctx, "sh", "-c", "grep -rl 'Domains=.*test' "+linuxResolvedDropinDir+" 2>/dev/null"); err != nil {
		detail = append(detail, linuxResolvedDropinDir+": no *.test routing domain found")
	}
	detail = append(detail,
		"to fix manually (systemd-resolved, e.g. Omarchy - see README's Omarchy section for other DNS stacks):",
		"  sudo mkdir -p /etc/dnsmasq.d",
		"  "+dnsmasqConfManualCmd(),
		"  sudo sed -i -e 's/^#conf-dir=\\/etc\\/dnsmasq.d\\/,\\*\\.conf$/conf-dir=\\/etc\\/dnsmasq.d\\/,*.conf/' -e 's/^#bind-interfaces$/bind-interfaces/' /etc/dnsmasq.conf",
		"  sudo systemctl reset-failed dnsmasq; sudo systemctl enable --now dnsmasq",
		"  printf '[Resolve]\\nDNS=127.0.0.1\\nDomains=~test\\n' | sudo tee "+linuxResolvedDropinPath,
		"  sudo systemctl restart systemd-resolved",
	)

	return CheckResult{
		Name:    "dns-test-tld",
		Status:  SeverityFail,
		Summary: ".test does not resolve to 127.0.0.1",
		Detail:  detail,
		Fix: &FixAction{
			Label:        "install dnsmasq and route .test via systemd-resolved",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyDNSFixLinux(ctx, sh, tty)
			},
		},
	}
}

// applyDNSFixLinux mirrors the steps in the README's Omarchy section. It
// only supports systemd-resolved setups; it refuses rather than guessing at
// NetworkManager/nmcli commands for other DNS stacks.
func applyDNSFixLinux(ctx context.Context, sh, tty shellExecutor) error {
	if _, err := sh(ctx, "systemctl", "is-active", "systemd-resolved"); err != nil {
		return fmt.Errorf("systemd-resolved not active: this automatic fix only supports systemd-resolved setups (e.g. Omarchy); see README's Omarchy section for manual steps on other DNS stacks: %w", err)
	}
	script := "set -e\n" +
		"mkdir -p /etc/dnsmasq.d\n" +
		dnsmasqConfWriteCmd() + "\n" +
		`sed -i -e 's/^#conf-dir=\/etc\/dnsmasq.d\/,\*\.conf$/conf-dir=\/etc\/dnsmasq.d\/,*.conf/' -e 's/^#bind-interfaces$/bind-interfaces/' /etc/dnsmasq.conf` + "\n" +
		"systemctl reset-failed dnsmasq || true\n" +
		"systemctl enable --now dnsmasq\n" +
		"mkdir -p " + linuxResolvedDropinDir + "\n" +
		`printf '[Resolve]\nDNS=127.0.0.1\nDomains=~test\n' > ` + linuxResolvedDropinPath + "\n" +
		"systemctl restart systemd-resolved\n"
	out, err := tty(ctx, "sudo", "sh", "-c", script)
	if err != nil {
		return fmt.Errorf("dns fix: %w: %s", err, out)
	}
	return nil
}

// checkTrustStoreLinux is the Linux counterpart of checkTrustStoreDarwin.
// There is no single "system keychain" on Linux; instead distros extract
// trusted anchors into a flat PEM bundle at one of a few well-known paths
// (linuxCABundlePaths). The check reads whichever bundle exists and looks
// for a certificate block whose SHA-256(DER) matches the CA fetched from
// the running container.
func checkTrustStoreLinux(ctx context.Context, a *App, sh shellExecutor, tty shellExecutor) CheckResult {
	state, _ := a.docker.ContainerState(a.inst.ContainerName)
	if state != docker.StateRunning {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeveritySkip,
			Summary: "container not running",
		}
	}
	cert, err := a.docker.Exec(ctx, a.inst.ContainerName, []string{"cat", "/data/caddy/pki/authorities/local/root.crt"})
	if err != nil {
		return CheckResult{
			Name:    "trust-store",
			Status:  SeverityWarn,
			Summary: "no root CA yet (register a backend to trigger provisioning)",
			Detail:  []string{err.Error()},
		}
	}
	derBytes := cert
	if block, _ := pem.Decode(cert); block != nil {
		derBytes = block.Bytes
	}
	sum := sha256.Sum256(derBytes)
	wantSHA := strings.ToUpper(hex.EncodeToString(sum[:]))

	var checkedPaths []string
	for _, path := range linuxCABundlePaths {
		if _, err := sh(ctx, "test", "-f", path); err != nil {
			continue
		}
		checkedPaths = append(checkedPaths, path)
		bundle, err := sh(ctx, "cat", path)
		if err != nil {
			continue
		}
		if bundleContainsSHA256(bundle, wantSHA) {
			return CheckResult{
				Name:    "trust-store",
				Status:  SeverityPass,
				Summary: "Caddy root CA trusted in system CA bundle (" + path + ")",
			}
		}
	}

	detail := []string{
		"expected SHA-256 (DER): " + wantSHA,
		"cert source: " + a.inst.ContainerName + ":/data/caddy/pki/authorities/local/root.crt",
		"checked bundles: " + strings.Join(checkedPaths, ", "),
		"to fix manually (Arch/Omarchy/Fedora - p11-kit):",
		"  docker exec " + a.inst.ContainerName + " cat /data/caddy/pki/authorities/local/root.crt > /tmp/caddy_shack_root.crt",
		"  sudo trust anchor /tmp/caddy_shack_root.crt",
		"to fix manually (Debian/Ubuntu):",
		"  docker exec " + a.inst.ContainerName + " cat /data/caddy/pki/authorities/local/root.crt | sudo tee /usr/local/share/ca-certificates/caddy_shack.crt",
		"  sudo update-ca-certificates",
	}
	return CheckResult{
		Name:    "trust-store",
		Status:  SeverityFail,
		Summary: "Caddy root CA not found in system CA bundle",
		Detail:  detail,
		Fix: &FixAction{
			Label:        "trust Caddy root CA in the system CA store",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyTrustStoreFixLinux(ctx, sh, tty, cert)
			},
		},
	}
}

// bundleContainsSHA256 reports whether any CERTIFICATE PEM block in bundle
// hashes (SHA-256 over its DER bytes) to want (uppercase hex).
func bundleContainsSHA256(bundle []byte, want string) bool {
	rest := bundle
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if block.Type == "CERTIFICATE" {
			sum := sha256.Sum256(block.Bytes)
			if strings.ToUpper(hex.EncodeToString(sum[:])) == want {
				return true
			}
		}
	}
}

// applyTrustStoreFixLinux prefers p11-kit's trust (Arch/Omarchy/Fedora),
// falling back to update-ca-certificates (Debian/Ubuntu). Refuses if
// neither is present rather than guessing at a third mechanism.
func applyTrustStoreFixLinux(ctx context.Context, sh, tty shellExecutor, cert []byte) error {
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

	if _, err := sh(ctx, "sh", "-c", "command -v trust"); err == nil {
		out, err := tty(ctx, "sudo", "trust", "anchor", tmp.Name())
		if err != nil {
			return fmt.Errorf("trust anchor: %w: %s", err, out)
		}
		return nil
	}
	if _, err := sh(ctx, "sh", "-c", "command -v update-ca-certificates"); err == nil {
		out, err := tty(ctx, "sudo", "sh", "-c",
			"cp "+tmp.Name()+" /usr/local/share/ca-certificates/caddy_shack.crt && update-ca-certificates")
		if err != nil {
			return fmt.Errorf("update-ca-certificates: %w: %s", err, out)
		}
		return nil
	}
	return fmt.Errorf("neither 'trust' (p11-kit) nor 'update-ca-certificates' found; see README's Omarchy section for manual steps")
}

func checkSharedNetwork(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "docker daemon unreachable"}
	}
	cstate, _ := a.docker.ContainerState(a.inst.ContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "container not running"}
	}
	st, err := state.Load(a.paths.StatePath)
	if err != nil {
		return CheckResult{Name: "shared-network", Status: SeveritySkip, Summary: "state unreadable; covered by state-integrity"}
	}
	containerBackends := 0
	for _, b := range st.Backends {
		host, _, err := net.SplitHostPort(b.Upstream)
		if err == nil && host != UpstreamHost {
			containerBackends++
		}
	}
	if containerBackends == 0 {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityPass,
			Summary: "no container-mode backends; shared network not required",
		}
	}
	attached, err := a.docker.NetworkAttached(a.inst.NetworkName, a.inst.ContainerName)
	if err != nil {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityWarn,
			Summary: "cannot inspect container networks",
			Detail:  []string{err.Error()},
		}
	}
	if attached {
		return CheckResult{
			Name:    "shared-network",
			Status:  SeverityPass,
			Summary: fmt.Sprintf("caddy attached to %q (%d container-mode backend(s))", a.inst.NetworkName, containerBackends),
		}
	}
	return CheckResult{
		Name:    "shared-network",
		Status:  SeverityFail,
		Summary: fmt.Sprintf("caddy not attached to %q but %d container-mode backend(s) need it", a.inst.NetworkName, containerBackends),
		Detail:  []string{"container-mode (--container) registrations are unreachable until caddy joins this network"},
		Fix: &FixAction{
			Label: "create and attach shared network ('docker network connect " + a.inst.NetworkName + " " + a.inst.ContainerName + "')",
			Apply: func(_ context.Context) error { return a.ensureSharedNetwork() },
		},
	}
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
	cstate, _ := a.docker.ContainerState(a.inst.ContainerName)
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
	// On native Linux host-mode backends are reached through the relay, so
	// that is where they are probed from.
	relayed := a.hostNetwork().Kind == EngineNativeLinux
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
		if relayed && host == UpstreamHost {
			// Probe the hop the relay makes: the host's own loopback, on
			// either family, as the relay's localhost dial does.
			if probeBackendFrom(ctx, a, a.inst.RelayContainerName, "127.0.0.1", port) ||
				probeBackendFrom(ctx, a, a.inst.RelayContainerName, "::1", port) {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s -> localhost:%s (via %s): unreachable", n, port, a.inst.RelayContainerName))
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
// Caddy container. Tries `nc -z` first; on any error, falls back to wget
// --spider.
func probeBackend(ctx context.Context, a *App, host, port string) bool {
	return probeBackendFrom(ctx, a, a.inst.ContainerName, host, port)
}

// probeBackendFrom is probeBackend from an arbitrary container (the relay,
// on native Linux).
func probeBackendFrom(ctx context.Context, a *App, container, host, port string) bool {
	if _, err := a.docker.Exec(ctx, container,
		[]string{"nc", "-z", "-w", "3", host, port}); err == nil {
		return true
	}
	url := "http://" + net.JoinHostPort(host, port) + "/"
	_, err := a.docker.Exec(ctx, container,
		[]string{"wget", "-q", "--spider", "--tries", "1", "--timeout", "3", url})
	return err == nil
}

// publishedPorts is the list of loopback ports this instance publishes from
// the container.
func (i Instance) publishedPorts() []int {
	return []int{i.HTTPPort, i.HTTPSPort, i.AdminPort}
}

func checkPortAvailability(ctx context.Context, a *App) CheckResult {
	return checkPortAvailabilityOn(ctx, a, a.inst.publishedPorts())
}

// checkPortAvailabilityOn is the test seam. Tests call it with a free
// ephemeral port so they don't need root for 80/443.
// Uses DialTimeout rather than net.Listen to avoid EACCES on ports 80/443
// when running as a non-root user.
// joinPorts renders ports as "80, 443, 2019".
func joinPorts(ports []int) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = strconv.Itoa(p)
	}
	return strings.Join(s, ", ")
}

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
			Summary: fmt.Sprintf("%s free or held by caddy_shack", joinPorts(ports)),
		}
	}
	// If our container is running, assume it holds the ports (the spec's
	// "free or already held by caddy_shack" wording). PASS with a note.
	state, _ := a.docker.ContainerState(a.inst.ContainerName)
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

func checkConfigDrift(ctx context.Context, a *App) CheckResult {
	cstate, _ := a.docker.ContainerState(a.inst.ContainerName)
	if cstate != docker.StateRunning {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeveritySkip,
			Summary: "container not running",
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
	d, err := a.liveDrift(ctx, st)
	if err != nil {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityFail,
			Summary: "cannot fetch live config",
			Detail:  []string{err.Error()},
		}
	}
	if d.InSync {
		return CheckResult{
			Name:    "config-drift",
			Status:  SeverityPass,
			Summary: "live Caddy config matches state.json",
		}
	}
	return CheckResult{
		Name:    "config-drift",
		Status:  SeverityFail,
		Summary: "live Caddy config differs from state.json",
		Detail:  append(DriftDetail(d), "'caddy_shack sync' re-pushes state.json to Caddy"),
		Fix: &FixAction{
			Label: "re-push state.json to Caddy ('caddy_shack sync')",
			Apply: func(ctx context.Context) error {
				_, err := a.reconcile(ctx)
				return err
			},
		},
	}
}

// DriftDetail renders d as human-readable lines, shared by doctor and
// status.
func DriftDetail(d Drift) []string {
	var out []string
	if !d.HTTPApp {
		out = append(out, "no http app: Caddy serves nothing on 80/443")
	}
	if !d.TLSApp {
		out = append(out, "no tls app: Caddy issues no certificates")
	}
	if len(d.Missing) > 0 {
		out = append(out, "registered but not live: "+strings.Join(d.Missing, ", "))
	}
	if len(d.Changed) > 0 {
		out = append(out, "live route differs from registry: "+strings.Join(d.Changed, ", "))
	}
	if len(d.Unknown) > 0 {
		out = append(out, "live but not registered: "+strings.Join(d.Unknown, ", "))
	}
	if d.CertsDiffer {
		out = append(out, "loaded certificates differ from the certificate store")
	}
	if len(out) == 0 {
		out = append(out, "live config differs from what the registry renders")
	}
	return out
}
