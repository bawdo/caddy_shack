// Package lanip finds the machine's LAN IPv4 address using only the
// standard library.
package lanip

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Iface describes a network interface and its addresses.
type Iface struct {
	Name     string
	Up       bool
	Loopback bool
	Addrs    []netip.Prefix
}

// Source supplies interface and routing information.
type Source interface {
	Interfaces() ([]Iface, error)
	RouteSource() (netip.Addr, error) // default-route source address
}

// Candidate is a usable LAN address on a physical interface.
type Candidate struct {
	Iface string
	IP    netip.Addr
}

// Result is the outcome of a successful Discover.
type Result struct {
	IP   netip.Addr
	Note string // non-empty when the default route was not used
}

// ErrNoCandidate means no private IPv4 address was found on a physical interface.
var ErrNoCandidate = errors.New("no private IPv4 address on a physical interface")

// AmbiguousError is returned when several candidates exist and none is
// selected by the default route.
type AmbiguousError struct {
	Candidates []Candidate
}

func (e *AmbiguousError) Error() string {
	parts := make([]string, len(e.Candidates))
	for i, c := range e.Candidates {
		parts[i] = fmt.Sprintf("%s (%s)", c.IP, c.Iface)
	}
	return "more than one LAN address found: " + strings.Join(parts, ", ") + "; choose one with --ip"
}

var excludedPrefixes = []string{"utun", "tun", "wg", "tailscale", "docker", "br-", "veth", "bridge", "vmnet", "awdl", "llw"}

var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
}

func isPrivateV4(a netip.Addr) bool {
	if !a.Is4() {
		return false
	}
	for _, p := range privateRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func excluded(name string) bool {
	for _, p := range excludedPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Candidates lists private IPv4 addresses on up, non-loopback, physical interfaces.
func Candidates(src Source) ([]Candidate, error) {
	ifaces, err := src.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, i := range ifaces {
		if !i.Up || i.Loopback || excluded(i.Name) {
			continue
		}
		for _, p := range i.Addrs {
			if a := p.Addr(); isPrivateV4(a) {
				out = append(out, Candidate{Iface: i.Name, IP: a})
			}
		}
	}
	return out, nil
}

// Discover picks the LAN address: the default-route source if it is a
// candidate, otherwise the sole candidate, otherwise an error.
func Discover(src Source) (Result, error) {
	cands, err := Candidates(src)
	if err != nil {
		return Result{}, err
	}
	if route, rerr := src.RouteSource(); rerr == nil {
		for _, c := range cands {
			if c.IP == route {
				return Result{IP: c.IP}, nil
			}
		}
	}
	switch len(cands) {
	case 0:
		return Result{}, ErrNoCandidate
	case 1:
		c := cands[0]
		return Result{IP: c.IP, Note: fmt.Sprintf("default route not on a LAN interface; using %s on %s", c.IP, c.Iface)}, nil
	}
	return Result{}, &AmbiguousError{Candidates: cands}
}

// Present reports whether ip is assigned to any interface (candidate or not).
func Present(src Source, ip netip.Addr) (bool, error) {
	ifaces, err := src.Interfaces()
	if err != nil {
		return false, err
	}
	for _, i := range ifaces {
		for _, p := range i.Addrs {
			if p.Addr() == ip {
				return true, nil
			}
		}
	}
	return false, nil
}
