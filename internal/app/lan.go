package app

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"

	"github.com/bawdo/caddy_shack/internal/caddyapi"
	"github.com/bawdo/caddy_shack/internal/docker"
	"github.com/bawdo/caddy_shack/internal/lanip"
	"github.com/bawdo/caddy_shack/internal/state"
)

const (
	// LANContainerName is the haproxy forwarder that holds the LAN IP port
	// bindings.
	LANContainerName = "caddy_shack_lan"
	// LANImage is the pinned forwarder image.
	LANImage = "haproxy:3.2-alpine"
)

// LANResolver finds and checks the machine's LAN address. The App depends on
// this rather than lanip.Source so tests can stub it directly.
type LANResolver interface {
	Discover() (lanip.Result, error)
	Present(ip netip.Addr) (bool, error)
}

type systemLANResolver struct{}

func (systemLANResolver) Discover() (lanip.Result, error) { return lanip.Discover(lanip.System()) }
func (systemLANResolver) Present(ip netip.Addr) (bool, error) {
	return lanip.Present(lanip.System(), ip)
}

// WithLANResolver replaces the LAN resolver (for tests) and returns a.
func (a *App) WithLANResolver(r LANResolver) *App {
	a.lan = r
	return a
}

// haproxyConfig forwards raw TCP on 8443 and 8080 to Caddy's LAN listeners,
// prefixing each connection with PROXY protocol v2 so Caddy sees the real
// client address. init-addr none lets haproxy start before Caddy exists.
func haproxyConfig(caddyHost string) string {
	return strings.ReplaceAll(`global
  log stdout format raw local0 warning
defaults
  mode tcp
  timeout connect 5s
  timeout client 1h
  timeout server 1h
resolvers docker
  nameserver dns 127.0.0.11:53
  hold valid 10s
frontend https
  bind :8443
  default_backend caddy_https
frontend http
  bind :8080
  default_backend caddy_http
backend caddy_https
  server caddy <caddy>:8443 send-proxy-v2 resolvers docker init-addr none
backend caddy_http
  server caddy <caddy>:8080 send-proxy-v2 resolvers docker init-addr none
`, "<caddy>", caddyHost)
}

// lanContainerSpec is the forwarder. The config travels as $0 of the sh -c
// script so no shell quoting is involved.
func lanContainerSpec(inst Instance, ip string) docker.ContainerSpec {
	return docker.ContainerSpec{
		Name:        inst.LANContainerName,
		Image:       LANImage,
		NetworkMode: inst.NetworkName,
		PortMappings: []docker.PortMapping{
			{HostAddr: ip, HostPort: inst.LANHTTPSPort, ContainerPort: caddyapi.LANHTTPSListenPort},
			{HostAddr: ip, HostPort: inst.LANHTTPPort, ContainerPort: caddyapi.LANHTTPListenPort},
		},
		Command: []string{
			"sh", "-c",
			"printf '%s' \"$0\" > /tmp/haproxy.cfg && exec haproxy -W -db -f /tmp/haproxy.cfg",
			haproxyConfig(inst.ContainerName),
		},
	}
}

// ensureLAN makes the forwarder match the LAN settings in s. Disabled means
// no forwarder. If the LAN IP is not on any interface Docker is not touched
// and a wrapped ErrLANDown is returned. The forwarder is stateless, so a
// mismatched one is replaced.
func (a *App) ensureLAN(s *state.State) error {
	if !s.LANEnabled() {
		return a.removeLAN()
	}
	ip, err := netip.ParseAddr(s.LAN.IP)
	if err != nil {
		return fmt.Errorf("LAN IP %q in state is invalid: %w", s.LAN.IP, err)
	}
	ok, err := a.lan.Present(ip)
	if err != nil {
		return fmt.Errorf("check LAN IP %s: %w", ip, err)
	}
	if !ok {
		return &lanDownError{IP: ip.String()}
	}
	name := a.inst.LANContainerName
	if err := a.docker.EnsureNetwork(a.inst.NetworkName); err != nil {
		return err
	}
	spec := lanContainerSpec(a.inst, s.LAN.IP)
	st, err := a.docker.ContainerState(name)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st != docker.StateAbsent {
		info, err := a.docker.InspectContainer(name)
		if err != nil {
			return fmt.Errorf("inspect LAN forwarder: %w", err)
		}
		if len(specDrift(spec, info)) == 0 {
			if st == docker.StateStopped {
				if err := a.docker.Start(name); err != nil {
					return a.lanStartError(s.LAN.IP, err)
				}
			}
			return nil
		}
		if err := a.docker.Remove(name); err != nil {
			return err
		}
	}
	if err := a.docker.CreateAndStart(spec); err != nil {
		_ = a.docker.Remove(name)
		return a.lanStartError(s.LAN.IP, err)
	}
	return nil
}

var addrInUse = regexp.MustCompile(`(?i)address already in use|port is already allocated`)

// lanStartFailure is the forwarder failing to be created or started, most
// often because another process holds the LAN address and port. lan enable
// reports it as an error; start, sync and friends turn it into a notice.
type lanStartFailure struct {
	msg string
	err error
}

func (e *lanStartFailure) Error() string { return e.msg + ": " + e.err.Error() }
func (e *lanStartFailure) Unwrap() error { return e.err }

// lanStartError turns Docker's create or start failure into a
// lanStartFailure naming the address and port.
func (a *App) lanStartError(ip string, err error) error {
	if addrInUse.MatchString(err.Error()) {
		port := a.inst.LANHTTPSPort
		for _, p := range []int{a.inst.LANHTTPSPort, a.inst.LANHTTPPort} {
			if strings.Contains(err.Error(), fmt.Sprintf("%s:%d", ip, p)) {
				port = p
				break
			}
		}
		return &lanStartFailure{msg: fmt.Sprintf("%s:%d is already in use by another process", ip, port), err: err}
	}
	return &lanStartFailure{
		msg: fmt.Sprintf("could not start the LAN forwarder on %s:%d and %s:%d", ip, a.inst.LANHTTPSPort, ip, a.inst.LANHTTPPort),
		err: err,
	}
}

// stopLAN stops the forwarder if it is running. Safe when absent.
func (a *App) stopLAN() error {
	st, err := a.docker.ContainerState(a.inst.LANContainerName)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st.CanStop() {
		return a.docker.Stop(a.inst.LANContainerName)
	}
	return nil
}

// removeLAN deletes the forwarder if it exists.
func (a *App) removeLAN() error {
	st, err := a.docker.ContainerState(a.inst.LANContainerName)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st == docker.StateAbsent {
		return nil
	}
	return a.docker.Remove(a.inst.LANContainerName)
}

// lanDownError is ErrLANDown carrying the address that went missing, so the
// notice can name it. errors.Is(err, ErrLANDown) holds.
type lanDownError struct{ IP string }

func (e *lanDownError) Error() string { return fmt.Sprintf("%s: %s", e.IP, ErrLANDown) }
func (e *lanDownError) Unwrap() error { return ErrLANDown }

// noteLANDown turns ErrLANDown and a forwarder that could not be created or
// started into a notice (nil), and passes anything else through, so start,
// sync and friends carry on without the forwarder.
func (a *App) noteLANDown(err error) error {
	var startFail *lanStartFailure
	if errors.As(err, &startFail) {
		a.notef("notice: LAN access is off: %v. Run 'caddy_shack doctor' for details.\n", startFail)
		return nil
	}
	if !errors.Is(err, ErrLANDown) {
		return err
	}
	ip := "the stored LAN IP"
	var down *lanDownError
	if errors.As(err, &down) {
		ip = down.IP
	}
	a.notef("notice: LAN access is off: %s is not on any interface. Reconnect to that network and run "+
		"'caddy_shack restart' (discovered mode rebinds automatically; pinned mode: 'caddy_shack lan enable --ip <new>').\n", ip)
	return nil
}

// ensureLANFromState brings the forwarder in line with the saved LAN
// settings, for use after Caddy's config is in place. With no LAN settings
// at all Docker is not touched. LAN down, or a forwarder that cannot bind,
// is a notice, not an error.
func (a *App) ensureLANFromState() error {
	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}
	if s.LAN == nil {
		return nil
	}
	return a.noteLANDown(a.ensureLAN(s))
}

// rediscoverLAN re-runs LAN discovery when the saved address was discovered
// (not pinned), so a restart follows the machine to a new network. A changed
// address is saved and announced. A failed discovery is a notice: the old
// address stays and ensureLAN reports it if it is gone.
func (a *App) rediscoverLAN() error {
	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}
	if !s.LANEnabled() || s.LAN.Mode != state.LANModeDiscovered {
		return nil
	}
	res, err := a.lan.Discover()
	if err != nil {
		a.notef("notice: could not rediscover the LAN IP, keeping %s: %v\n", s.LAN.IP, err)
		return nil
	}
	newIP := res.IP.String()
	if newIP == s.LAN.IP {
		return nil
	}
	updated := s.Clone()
	updated.LAN.IP = newIP
	if err := state.Save(a.paths.StatePath, updated); err != nil {
		return err
	}
	a.notef("LAN IP changed %s -> %s; update your DNS A record\n", s.LAN.IP, newIP)
	return nil
}

// upgradeLAN refreshes the forwarder image. The forwarder is stateless, so a
// stale one is simply replaced. Does nothing when LAN is not enabled.
func (a *App) upgradeLAN() error {
	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}
	if !s.LANEnabled() {
		return nil
	}
	if err := a.docker.PullImage(LANImage); err != nil {
		return err
	}
	st, err := a.docker.ContainerState(a.inst.LANContainerName)
	if err != nil {
		return fmt.Errorf("inspect LAN forwarder: %w", err)
	}
	if st != docker.StateAbsent {
		newID, err := a.docker.ImageID(LANImage)
		if err != nil {
			return fmt.Errorf("image id: %w", err)
		}
		curID, err := a.docker.ContainerImageID(a.inst.LANContainerName)
		if err != nil {
			return fmt.Errorf("container image id: %w", err)
		}
		if newID == curID {
			return nil
		}
		if err := a.removeLAN(); err != nil {
			return err
		}
	}
	return a.noteLANDown(a.ensureLAN(s))
}

// LANEnableInput holds the lan enable arguments.
type LANEnableInput struct {
	IP      string     // empty means discover
	Suffix  string     // empty means keep the stored suffix; required if none stored
	Confirm PromptFunc // nil means proceed without asking (--yes)
}

// LANEnableResult describes what lan enable bound.
type LANEnableResult struct {
	IP     string
	Mode   state.LANMode
	Suffix string
	Note   string
}

var lanNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// validLANName checks a LAN suffix: a lowercase DNS name that is not in the
// .test space, which belongs to the local CA.
func validLANName(name string) error {
	if len(name) > 253 || !lanNameRE.MatchString(name) {
		return fmt.Errorf("%w: %q is not a valid DNS name", ErrInvalidArgs, name)
	}
	if name == "test" || strings.HasSuffix(name, ".test") {
		return fmt.Errorf("%w: %q is in the .test space, which is reserved for local names", ErrInvalidArgs, name)
	}
	return nil
}

// resolveLANIP picks the address to bind: the pinned one if given, else the
// discovered one.
func (a *App) resolveLANIP(pin string) (string, state.LANMode, string, error) {
	if pin == "" {
		res, err := a.lan.Discover()
		if err != nil {
			return "", "", "", fmt.Errorf("%w: %w", ErrInvalidArgs, err)
		}
		return res.IP.String(), state.LANModeDiscovered, res.Note, nil
	}
	ip, err := netip.ParseAddr(pin)
	if err != nil || !ip.Is4() || ip.IsLoopback() {
		return "", "", "", fmt.Errorf("%w: --ip %q must be a non-loopback IPv4 address", ErrInvalidArgs, pin)
	}
	ok, err := a.lan.Present(ip)
	if err != nil {
		return "", "", "", fmt.Errorf("check LAN IP %s: %w", ip, err)
	}
	if !ok {
		return "", "", "", fmt.Errorf("%w: %s is not assigned to any interface on this machine", ErrInvalidArgs, ip)
	}
	return ip.String(), state.LANModePinned, "", nil
}

// pullLANImage fetches the forwarder image. A failed pull is tolerated when
// the image is already cached, so enable still works offline.
func (a *App) pullLANImage() error {
	err := a.docker.PullImage(LANImage)
	if err == nil {
		return nil
	}
	if id, idErr := a.docker.ImageID(LANImage); idErr == nil && id != "" {
		a.notef("notice: could not pull %s, using the cached image: %v\n", LANImage, err)
		return nil
	}
	return fmt.Errorf("pull LAN forwarder image: %w", err)
}

// LANEnable switches LAN mode on: settings are saved and pushed to Caddy
// first, then the forwarder is started. Backends' LANDomain values are left
// alone, so a re-enable with a new suffix does not rename anything.
func (a *App) LANEnable(ctx context.Context, in LANEnableInput) (LANEnableResult, error) {
	if err := a.requireRunning(); err != nil {
		return LANEnableResult{}, err
	}
	if in.Suffix != "" {
		if err := validLANName(in.Suffix); err != nil {
			return LANEnableResult{}, err
		}
	}
	peek, err := loadState(a.paths.StatePath)
	if err != nil {
		return LANEnableResult{}, err
	}
	if in.Suffix == "" && (peek.LAN == nil || peek.LAN.Suffix == "") {
		return LANEnableResult{}, fmt.Errorf("%w: --suffix is required the first time", ErrInvalidArgs)
	}
	ip, mode, note, err := a.resolveLANIP(in.IP)
	if err != nil {
		return LANEnableResult{}, err
	}
	if in.Confirm != nil && !in.Confirm(fmt.Sprintf("Bind LAN access to %s (%s)? ", ip, mode)) {
		return LANEnableResult{}, errors.New("cancelled")
	}
	// Pull before taking the lock: creating the forwarder would otherwise
	// pull implicitly with every other command locked out.
	if err := a.pullLANImage(); err != nil {
		return LANEnableResult{}, err
	}

	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return LANEnableResult{}, err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return LANEnableResult{}, err
	}
	suffix := in.Suffix
	if suffix == "" {
		if s.LAN == nil || s.LAN.Suffix == "" {
			return LANEnableResult{}, fmt.Errorf("%w: --suffix is required the first time", ErrInvalidArgs)
		}
		suffix = s.LAN.Suffix
	}
	updated := s.Clone()
	updated.LAN = &state.LAN{Enabled: true, IP: ip, Mode: mode, Suffix: suffix}
	if err := a.commit(ctx, s, updated); err != nil {
		return LANEnableResult{}, err
	}
	if err := a.ensureSharedNetwork(); err != nil {
		return LANEnableResult{}, fmt.Errorf("ensure shared network: %w", err)
	}
	if err := a.ensureLAN(updated); err != nil {
		return LANEnableResult{}, err
	}
	return LANEnableResult{IP: ip, Mode: mode, Suffix: suffix, Note: note}, nil
}

// LANDisable removes the forwarder first (so nothing is left listening),
// then pushes the config without LAN servers and records Enabled=false. The
// stored IP, mode and suffix are kept for the next enable.
func (a *App) LANDisable(ctx context.Context) error {
	lock, err := acquireStateLock(a.paths.LockPath)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()

	s, err := loadState(a.paths.StatePath)
	if err != nil {
		return err
	}
	if err := a.removeLAN(); err != nil {
		return err
	}
	if s.LAN == nil || !s.LAN.Enabled {
		return nil
	}
	updated := s.Clone()
	updated.LAN.Enabled = false
	// With Caddy down there is nothing to push: record the change and let the
	// next start or sync render the config from state.
	err = a.requireRunning()
	if err == nil {
		err = a.commit(ctx, s, updated)
	}
	if errors.Is(err, ErrCaddyNotRunning) {
		return state.Save(a.paths.StatePath, updated)
	}
	return err
}
