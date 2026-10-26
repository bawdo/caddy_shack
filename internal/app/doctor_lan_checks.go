package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/state"
)

const lanProbeTimeout = 3 * time.Second

// lanTLSDial connects over TCP, then completes a TLS handshake against addr
// with cfg and closes the connection. A nil return means the handshake
// completed. A TCP failure comes back as the dial's *net.OpError (Op
// "dial"); a server refusing the handshake as a TLS alert (see
// isTLSAlert). A variable so tests can stub it.
var lanTLSDial = func(ctx context.Context, addr string, cfg *tls.Config) error {
	ctx, cancel := context.WithTimeout(ctx, lanProbeTimeout)
	defer cancel()
	raw, err := (&net.Dialer{Timeout: lanProbeTimeout}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	conn := tls.Client(raw, cfg)
	defer func() { _ = conn.Close() }()
	return conn.HandshakeContext(ctx)
}

// isTLSAlert reports whether err is the server refusing the handshake with
// a TLS alert ("remote error: tls: ..."), as opposed to a failed dial, a
// reset, a timeout or an EOF.
func isTLSAlert(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "remote error"
}

// lanLookupHost resolves a name through the system resolver. A variable so
// tests can stub it.
var lanLookupHost = func(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, lanProbeTimeout)
	defer cancel()
	return net.DefaultResolver.LookupHost(ctx, host)
}

// lanCheckState loads state for a LAN check. When the check should not run
// (state unreadable, or LAN off) it returns the SKIP result to report.
func (a *App) lanCheckState(name string) (*state.State, *CheckResult) {
	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return nil, &CheckResult{Name: name, Status: SeveritySkip, Summary: "state unreadable; covered by state-integrity"}
	}
	if !s.LANEnabled() {
		return nil, &CheckResult{Name: name, Status: SeveritySkip, Summary: "LAN mode off"}
	}
	return s, nil
}

// lanNames returns the registered LAN names, sorted.
func lanNames(s *state.State) []string {
	var out []string
	for _, b := range s.Backends {
		if b.LANDomain != "" {
			out = append(out, b.LANDomain)
		}
	}
	sort.Strings(out)
	return out
}

// testNames returns the registered .test domains, sorted.
func testNames(s *state.State) []string {
	var out []string
	for _, b := range s.Backends {
		if b.Domain != "" {
			out = append(out, b.Domain)
		}
	}
	sort.Strings(out)
	return out
}

// fixLAN rediscovers the address first when rediscover is set, then brings
// the forwarder in line with freshly loaded state.
func (a *App) fixLAN(rediscover bool) func(context.Context) error {
	return func(context.Context) error {
		if rediscover {
			if err := a.rediscoverLAN(); err != nil {
				return err
			}
		}
		s, err := loadState(a.paths.StatePath)
		if err != nil {
			return err
		}
		return a.ensureLAN(s)
	}
}

func checkLANIPPresent(_ context.Context, a *App) CheckResult {
	const name = "lan-ip-present"
	s, skip := a.lanCheckState(name)
	if skip != nil {
		return *skip
	}
	ip, err := netip.ParseAddr(s.LAN.IP)
	if err != nil {
		return CheckResult{Name: name, Status: SeverityFail, Summary: fmt.Sprintf("stored LAN IP %q is invalid", s.LAN.IP)}
	}
	ok, err := a.lan.Present(ip)
	if err != nil {
		return CheckResult{Name: name, Status: SeverityFail, Summary: "cannot check interfaces", Detail: []string{err.Error()}}
	}
	if ok {
		return CheckResult{Name: name, Status: SeverityPass, Summary: s.LAN.IP + " is on an interface"}
	}
	r := CheckResult{Name: name, Status: SeverityFail, Summary: s.LAN.IP + " is not on any interface"}
	if s.LAN.Mode == state.LANModeDiscovered {
		r.Detail = []string{"rediscover the address and rebind the forwarder"}
		r.Fix = &FixAction{Label: "rediscover the LAN IP and rebind the forwarder", Apply: a.fixLAN(true)}
	} else {
		r.Detail = []string{"pinned address: reconnect to that network or run 'caddy_shack lan enable --ip <new>'"}
	}
	return r
}

func checkLANForwarder(_ context.Context, a *App) CheckResult {
	const name = "lan-forwarder"
	s, skip := a.lanCheckState(name)
	if skip != nil {
		return *skip
	}
	cname := a.inst.LANContainerName
	st, err := a.docker.ContainerState(cname)
	if err != nil {
		return CheckResult{Name: name, Status: SeverityFail, Summary: "cannot inspect forwarder", Detail: []string{err.Error()}}
	}
	fix := &FixAction{Label: "recreate the LAN forwarder", Apply: a.fixLAN(false)}
	switch st {
	case docker.StateAbsent:
		return CheckResult{Name: name, Status: SeverityFail, Summary: cname + " does not exist", Fix: fix}
	case docker.StateStopped:
		return CheckResult{Name: name, Status: SeverityFail, Summary: cname + " is stopped", Fix: fix}
	case docker.StateRestarting:
		return CheckResult{Name: name, Status: SeverityFail, Summary: cname + " is restarting (crash-looping)", Fix: fix}
	}
	info, err := a.docker.InspectContainer(cname)
	if err != nil {
		return CheckResult{Name: name, Status: SeverityFail, Summary: "cannot inspect forwarder", Detail: []string{err.Error()}}
	}
	if drift := specDrift(lanContainerSpec(a.inst, s.LAN.IP), info); len(drift) > 0 {
		return CheckResult{Name: name, Status: SeverityFail, Summary: cname + " differs from the expected setup", Detail: drift, Fix: fix}
	}
	return CheckResult{Name: name, Status: SeverityPass, Summary: cname + " is running as expected"}
}

func checkLANIPDrift(_ context.Context, a *App) CheckResult {
	const name = "lan-ip-drift"
	s, skip := a.lanCheckState(name)
	if skip != nil {
		return *skip
	}
	if s.LAN.Mode != state.LANModeDiscovered {
		return CheckResult{Name: name, Status: SeveritySkip, Summary: "pinned address"}
	}
	res, err := a.lan.Discover()
	if err != nil {
		return CheckResult{Name: name, Status: SeverityWarn, Summary: "cannot rediscover the LAN IP", Detail: []string{err.Error()}}
	}
	if res.IP.String() == s.LAN.IP {
		return CheckResult{Name: name, Status: SeverityPass, Summary: "stored LAN IP matches discovery"}
	}
	return CheckResult{
		Name:    name,
		Status:  SeverityWarn,
		Summary: fmt.Sprintf("stored LAN IP %s, discovered %s", s.LAN.IP, res.IP),
		Detail:  []string{"update your DNS A record after rebinding"},
		Fix:     &FixAction{Label: "rediscover the LAN IP and rebind the forwarder", Apply: a.fixLAN(true)},
	}
}

func checkLANPath(ctx context.Context, a *App) CheckResult {
	const name = "lan-path"
	s, skip := a.lanCheckState(name)
	if skip != nil {
		return *skip
	}
	names, tests := lanNames(s), testNames(s)
	if len(names) == 0 && len(tests) == 0 {
		return CheckResult{Name: name, Status: SeveritySkip, Summary: "no backends registered"}
	}
	addr := net.JoinHostPort(s.LAN.IP, strconv.Itoa(a.inst.LANHTTPSPort))
	var failures, warnings, passes []string

	if len(names) > 0 {
		pemStr, err := a.ExportCA(ctx, true)
		if err != nil {
			return CheckResult{Name: name, Status: SeverityFail, Summary: "cannot read the local CA", Detail: []string{err.Error()}}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(pemStr)) {
			return CheckResult{Name: name, Status: SeverityFail, Summary: "local CA is not valid PEM"}
		}
		cfg := &tls.Config{ServerName: names[0], RootCAs: pool, MinVersion: tls.VersionTLS12}
		if err := lanTLSDial(ctx, addr, cfg); err != nil {
			failures = append(failures, fmt.Sprintf("%s did not verify for %s: %v", addr, names[0], err))
		} else {
			passes = append(passes, names[0]+" is served")
		}
	}
	if len(tests) > 0 {
		// Verification is off on purpose: any completed handshake means
		// the LAN listener answers for a .test name.
		cfg := &tls.Config{ServerName: tests[0], InsecureSkipVerify: true, MinVersion: tls.VersionTLS12} //nolint:gosec // probing for a handshake, not trust
		// Only a TLS alert counts as a refusal: a failed dial or a dropped
		// connection says nothing about how .test names are handled.
		switch err := lanTLSDial(ctx, addr, cfg); {
		case err == nil:
			failures = append(failures, fmt.Sprintf("%s completed a handshake for %s: the LAN listener must refuse .test names", addr, tests[0]))
		case isTLSAlert(err):
			passes = append(passes, tests[0]+" is refused")
		default:
			warnings = append(warnings, fmt.Sprintf("could not probe %s: %v", addr, err))
		}
	}
	if len(failures) > 0 {
		return CheckResult{Name: name, Status: SeverityFail, Summary: "LAN listener check failed", Detail: append(failures, warnings...)}
	}
	if len(warnings) > 0 {
		return CheckResult{Name: name, Status: SeverityWarn, Summary: warnings[0], Detail: passes}
	}
	return CheckResult{Name: name, Status: SeverityPass, Summary: strings.Join(passes, "; ")}
}

func checkLANDNS(ctx context.Context, a *App) CheckResult {
	const name = "lan-dns"
	s, skip := a.lanCheckState(name)
	if skip != nil {
		return *skip
	}
	names := lanNames(s)
	if len(names) == 0 {
		return CheckResult{Name: name, Status: SeveritySkip, Summary: "no LAN names registered"}
	}
	var bad []string
	for _, n := range names {
		addrs, err := lanLookupHost(ctx, n)
		switch {
		case err != nil:
			bad = append(bad, fmt.Sprintf("%s does not resolve (%v)", n, err))
		case !slices.Contains(addrs, s.LAN.IP):
			bad = append(bad, fmt.Sprintf("%s resolves to %s, not %s", n, strings.Join(addrs, ", "), s.LAN.IP))
		}
	}
	if len(bad) > 0 {
		return CheckResult{
			Name:    name,
			Status:  SeverityWarn,
			Summary: fmt.Sprintf("%d of %d LAN names do not resolve to %s", len(bad), len(names), s.LAN.IP),
			Detail:  bad,
		}
	}
	return CheckResult{Name: name, Status: SeverityPass, Summary: "LAN names resolve to " + s.LAN.IP}
}

func checkLANFirewall(ctx context.Context, a *App, sh shellExecutor, goos string) CheckResult {
	const name = "lan-firewall"
	if _, skip := a.lanCheckState(name); skip != nil {
		return *skip
	}
	switch goos {
	case "linux":
		out, _ := sh(ctx, "systemctl", "is-active", "ufw")
		if strings.TrimSpace(string(out)) == "active" {
			return CheckResult{
				Name:    name,
				Status:  SeverityWarn,
				Summary: "ufw is active",
				Detail: []string{
					"Docker published ports bypass ufw, so ufw does not filter the LAN forwarder",
					"filter LAN traffic in the DOCKER-USER iptables chain instead",
				},
			}
		}
	case "darwin":
		out, _ := sh(ctx, "/usr/libexec/ApplicationFirewall/socketfilterfw", "--getblockall")
		if strings.Contains(strings.ToLower(string(out)), "enabled") {
			return CheckResult{
				Name:    name,
				Status:  SeverityWarn,
				Summary: "the macOS firewall is blocking incoming connections",
				Detail:  []string{"LAN clients may be unable to reach ports 80 and 443; allow Docker in System Settings > Network > Firewall"},
			}
		}
	default:
		return CheckResult{Name: name, Status: SeveritySkip, Summary: "unsupported platform"}
	}
	return CheckResult{Name: name, Status: SeverityPass, Summary: "no host firewall in the way"}
}
