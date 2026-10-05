package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
)

// lookupTimeout bounds the name lookups doctor makes. A healthy resolver
// answers (positively or with NXDOMAIN) in milliseconds; hitting this means
// lookups are hanging. Package var so tests can shrink it.
var lookupTimeout = 4 * time.Second

func checkDockerEngine(_ context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{Name: "docker-engine", Status: SeveritySkip, Summary: "docker daemon unreachable"}
	}
	hn := a.hostNetwork()
	switch hn.Kind {
	case EngineLoopbackForwarded:
		return CheckResult{
			Name:    "docker-engine",
			Status:  SeverityPass,
			Summary: fmt.Sprintf("%s (%s)", hn.Reason, hn.Kind),
			Detail: []string{
				"host-mode backends: " + UpstreamHost + ", forwarded by the engine to the host's loopback",
			},
		}
	case EngineNativeLinux:
		ufw := "ufw: unreadable"
		if st, err := readUFWStatus(); err == nil {
			ufw = "ufw: " + st.String() + " (no rule needed: the relay is a unix socket, not container->host TCP)"
		}
		return CheckResult{
			Name:    "docker-engine",
			Status:  SeverityPass,
			Summary: fmt.Sprintf("%s (%s)", hn.Reason, hn.Kind),
			Detail: []string{
				"host-mode backends: via " + RelayContainerName + " (host network) over " + RelaySocketPath +
					" to localhost:<port>",
				ufw,
			},
		}
	default:
		return CheckResult{
			Name:    "docker-engine",
			Status:  SeverityWarn,
			Summary: "could not classify the docker engine (" + hn.Kind.String() + ")",
			Detail: []string{
				hn.Reason,
				"using the Docker Desktop routing (" + UpstreamHost + ":<port>); host-mode backends only work if this engine forwards that name to the host's loopback",
			},
		}
	}
}

// checkContainerConfig verifies the containers match what this engine
// needs. Only native Linux has anything to check: the Caddy container must
// carry the relay socket volume and host-gateway entry (settings `docker
// run` fixes at creation, so an older container must be recreated), and
// the relay must be running with its socket visible to Caddy.
func checkContainerConfig(ctx context.Context, a *App) CheckResult {
	if err := a.docker.DaemonReachable(); err != nil {
		return CheckResult{Name: "container-config", Status: SeveritySkip, Summary: "docker daemon unreachable"}
	}
	if st, _ := a.docker.ContainerState(CaddyContainerName); st != docker.StateRunning {
		return CheckResult{Name: "container-config", Status: SeveritySkip, Summary: "container not running"}
	}
	hn := a.hostNetwork()
	if hn.Kind != EngineNativeLinux {
		return CheckResult{
			Name:    "container-config",
			Status:  SeverityPass,
			Summary: fmt.Sprintf("no extra settings needed on the %s engine", hn.Kind),
		}
	}

	drift, err := a.caddyDrift()
	if err != nil {
		return CheckResult{Name: "container-config", Status: SeverityFail, Summary: "cannot inspect container", Detail: []string{err.Error()}}
	}
	if len(drift) > 0 {
		return CheckResult{
			Name:    "container-config",
			Status:  SeverityFail,
			Summary: CaddyContainerName + " predates native Linux support; host-mode backends are unreachable",
			Detail: append(drift,
				"settings fixed at 'docker run' time; the container must be recreated",
				"volumes (certs and the local CA), networks and registered backends are kept"),
			Fix: &FixAction{
				Label: "recreate " + CaddyContainerName + " with the relay socket and host-gateway ('caddy_shack install')",
				Apply: func(ctx context.Context) error { return a.Install(ctx) },
			},
		}
	}

	ensureRelayFix := &FixAction{
		Label: "(re)create and start " + RelayContainerName,
		Apply: func(_ context.Context) error { return a.ensureRelay() },
	}
	rs, err := a.docker.ContainerState(RelayContainerName)
	if err != nil {
		return CheckResult{Name: "container-config", Status: SeverityFail, Summary: "cannot inspect relay", Detail: []string{err.Error()}}
	}
	if rs != docker.StateRunning {
		return CheckResult{
			Name:    "container-config",
			Status:  SeverityFail,
			Summary: fmt.Sprintf("%s is %s; host-mode backends are unreachable", RelayContainerName, rs),
			Fix:     ensureRelayFix,
		}
	}
	if info, err := a.docker.InspectContainer(RelayContainerName); err == nil {
		if d := specDrift(relayContainerSpec(CaddyImage), info); len(d) > 0 {
			return CheckResult{
				Name:    "container-config",
				Status:  SeverityFail,
				Summary: RelayContainerName + " was created with the wrong settings",
				Detail:  d,
				Fix:     ensureRelayFix,
			}
		}
	}
	if _, err := a.docker.Exec(ctx, CaddyContainerName, []string{"test", "-S", RelaySocketPath}); err != nil {
		return CheckResult{
			Name:    "container-config",
			Status:  SeverityFail,
			Summary: "relay socket " + RelaySocketPath + " not present in " + CaddyContainerName,
			Detail:  []string{"check 'docker logs " + RelayContainerName + "'"},
			Fix: &FixAction{
				Label: "recreate " + RelayContainerName,
				Apply: func(_ context.Context) error {
					if err := a.docker.Remove(RelayContainerName); err != nil {
						return err
					}
					return a.ensureRelay()
				},
			},
		}
	}
	return CheckResult{
		Name:    "container-config",
		Status:  SeverityPass,
		Summary: "relay running; " + CaddyContainerName + " has the relay socket and host-gateway",
	}
}

// checkHostGateway resolves host.docker.internal inside the Caddy container.
// Docker Desktop provides the name; native Linux gets it from the
// --add-host entry. A lookup that times out (rather than failing fast)
// points at a hanging DNS forwarder; see checkDNSNegativeLinux.
func checkHostGateway(ctx context.Context, a *App) CheckResult {
	if st, _ := a.docker.ContainerState(CaddyContainerName); st != docker.StateRunning {
		return CheckResult{Name: "host-gateway", Status: SeveritySkip, Summary: "container not running"}
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	start := time.Now()
	out, err := a.docker.Exec(lctx, CaddyContainerName, []string{"getent", "hosts", UpstreamHost})
	if err == nil {
		if f := strings.Fields(string(out)); len(f) > 0 {
			return CheckResult{
				Name:    "host-gateway",
				Status:  SeverityPass,
				Summary: fmt.Sprintf("%s resolves to %s inside %s", UpstreamHost, f[0], CaddyContainerName),
			}
		}
	}
	sev := SeverityFail
	if a.hostNetwork().Kind == EngineUnknown {
		sev = SeverityWarn
	}
	var detail []string
	if errors.Is(lctx.Err(), context.DeadlineExceeded) {
		detail = append(detail, fmt.Sprintf("lookup timed out after %s instead of failing fast: the container's DNS forwarder is hanging", time.Since(start).Round(time.Second)))
	} else if err != nil {
		detail = append(detail, err.Error())
	}
	if a.hostNetwork().Kind == EngineNativeLinux {
		detail = append(detail, "native Linux needs --add-host "+HostGatewayEntry+"; see the container-config check")
	}
	return CheckResult{
		Name:    "host-gateway",
		Status:  sev,
		Summary: UpstreamHost + " does not resolve inside " + CaddyContainerName,
		Detail:  detail,
	}
}

// linuxDNSMasqCaddyConf is the dnsmasq drop-in CaddyShack writes on Linux.
// dnsmasq answers .test and nothing else: no-resolv stops it forwarding to
// /etc/resolv.conf, which on systemd-resolved hosts is the 127.0.0.53 stub
// that sends queries straight back to dnsmasq (resolved's global DNS server
// receives every name, not just ~test). That loop makes every lookup for a
// non-existent name hang until timeout, on the host and in every container.
// local=/#/ answers non-.test names with an immediate NXDOMAIN; resolved
// still takes the positive answer from the uplink's DNS for real names.
const linuxDNSMasqCaddyConf = "# caddy_shack: answer .test only; never forward (avoids a resolved<->dnsmasq loop)\n" +
	"address=/.test/127.0.0.1\n" +
	"no-resolv\n" +
	"local=/#/\n"

// dnsmasqConfPrintf is a printf command emitting linuxDNSMasqCaddyConf.
// printf interprets the \n escapes; the content has no % or ' characters.
func dnsmasqConfPrintf() string {
	return "printf '" + strings.ReplaceAll(linuxDNSMasqCaddyConf, "\n", `\n`) + "'"
}

// dnsmasqConfWriteCmd writes linuxDNSMasqCaddyConf (run as root).
func dnsmasqConfWriteCmd() string {
	return dnsmasqConfPrintf() + " > " + linuxDNSMasqCaddyConfPath
}

// dnsmasqConfManualCmd is the copy-pasteable form for doctor's manual steps.
func dnsmasqConfManualCmd() string {
	return dnsmasqConfPrintf() + " | sudo tee " + linuxDNSMasqCaddyConfPath
}

// checkDNSNegativeLinux looks up a name that cannot exist and checks the
// answer comes back fast. On systemd-resolved hosts set up with an older
// caddy_shack dnsmasq drop-in, resolved and dnsmasq forward to each other,
// so such lookups hang (~100s) instead of returning NXDOMAIN.
func checkDNSNegativeLinux(ctx context.Context, _ *App, sh shellExecutor, tty shellExecutor) CheckResult {
	host := strings.TrimSuffix(randomTestSubdomain(), ".test") + ".caddy-shack-probe.example"
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	_, _ = sh(lctx, "getent", "ahosts", host)
	if !errors.Is(lctx.Err(), context.DeadlineExceeded) {
		return CheckResult{
			Name:    "dns-negative-lookups",
			Status:  SeverityPass,
			Summary: "lookups for unknown names fail fast",
		}
	}

	detail := []string{
		fmt.Sprintf("getent ahosts %s did not answer within %s", host, lookupTimeout),
		"likely cause: systemd-resolved sends every query to dnsmasq on 127.0.0.1, and dnsmasq forwards",
		"non-.test names back to resolved via /etc/resolv.conf, so unknown names loop until timeout",
		"(affects the host and every container, e.g. Caddy's upstream lookups)",
	}
	if _, err := sh(ctx, "grep", "-qx", "no-resolv", linuxDNSMasqCaddyConfPath); err != nil {
		detail = append(detail, linuxDNSMasqCaddyConfPath+": no 'no-resolv' line")
	}
	detail = append(detail,
		"to fix manually:",
		"  "+dnsmasqConfManualCmd(),
		"  sudo systemctl restart dnsmasq && sudo resolvectl flush-caches",
	)
	return CheckResult{
		Name:    "dns-negative-lookups",
		Status:  SeverityFail,
		Summary: "lookups for unknown names hang instead of failing fast",
		Detail:  detail,
		Fix: &FixAction{
			Label:        "stop dnsmasq forwarding non-.test names (write " + linuxDNSMasqCaddyConfPath + ", restart dnsmasq)",
			RequiresSudo: true,
			Apply: func(ctx context.Context) error {
				return applyDNSLoopFixLinux(ctx, tty)
			},
		},
	}
}

// applyDNSLoopFixLinux rewrites the dnsmasq drop-in with no-resolv and
// restarts dnsmasq. Leaves the systemd-resolved routing alone.
func applyDNSLoopFixLinux(ctx context.Context, tty shellExecutor) error {
	script := "set -e\n" +
		dnsmasqConfWriteCmd() + "\n" +
		"systemctl restart dnsmasq\n" +
		"resolvectl flush-caches || true\n"
	out, err := tty(ctx, "sudo", "sh", "-c", script)
	if err != nil {
		return fmt.Errorf("dns loop fix: %w: %s", err, out)
	}
	return nil
}
