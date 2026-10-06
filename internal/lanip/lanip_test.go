package lanip

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
)

type fakeSource struct {
	ifaces   []Iface
	route    netip.Addr
	routeErr error
}

func (f fakeSource) Interfaces() ([]Iface, error)     { return f.ifaces, nil }
func (f fakeSource) RouteSource() (netip.Addr, error) { return f.route, f.routeErr }

func iface(name string, addrs ...string) Iface {
	i := Iface{Name: name, Up: true}
	for _, a := range addrs {
		i.Addrs = append(i.Addrs, netip.MustParsePrefix(a))
	}
	return i
}

func TestDiscover(t *testing.T) {
	lo := iface("lo0", "127.0.0.1/8")
	lo.Loopback = true
	tests := []struct {
		name     string
		src      fakeSource
		wantIP   string
		wantNote string
		wantErr  error
		ambig    []Candidate
	}{
		{
			name: "route source on en0",
			src: fakeSource{
				ifaces: []Iface{lo, iface("en0", "192.168.1.12/24")},
				route:  netip.MustParseAddr("192.168.1.12"),
			},
			wantIP: "192.168.1.12",
		},
		{
			name: "route on tunnel falls back to single candidate",
			src: fakeSource{
				ifaces: []Iface{iface("utun4", "10.8.0.2/32"), iface("en0", "192.168.1.12/24")},
				route:  netip.MustParseAddr("10.8.0.2"),
			},
			wantIP:   "192.168.1.12",
			wantNote: "en0",
		},
		{
			name: "route error falls back to single candidate",
			src: fakeSource{
				ifaces:   []Iface{iface("en0", "192.168.1.12/24")},
				routeErr: errors.New("no route"),
			},
			wantIP:   "192.168.1.12",
			wantNote: "en0",
		},
		{
			name: "ambiguous",
			src: fakeSource{
				ifaces: []Iface{iface("en0", "192.168.1.12/24"), iface("en7", "192.168.1.50/24"), iface("utun4", "10.8.0.2/32")},
				route:  netip.MustParseAddr("10.8.0.2"),
			},
			ambig: []Candidate{
				{Iface: "en0", IP: netip.MustParseAddr("192.168.1.12")},
				{Iface: "en7", IP: netip.MustParseAddr("192.168.1.50")},
			},
		},
		{
			name: "only loopback and docker",
			src: fakeSource{
				ifaces: []Iface{lo, iface("docker0", "172.17.0.1/16")},
				route:  netip.MustParseAddr("172.17.0.1"),
			},
			wantErr: ErrNoCandidate,
		},
		{
			name: "bridge100 never a candidate",
			src: fakeSource{
				ifaces: []Iface{iface("bridge100", "192.168.64.1/24")},
				route:  netip.MustParseAddr("192.168.64.1"),
			},
			wantErr: ErrNoCandidate,
		},
		{
			name: "public address never a candidate",
			src: fakeSource{
				ifaces: []Iface{iface("en0", "203.0.113.5/24")},
				route:  netip.MustParseAddr("203.0.113.5"),
			},
			wantErr: ErrNoCandidate,
		},
		{
			name: "down interface is not a candidate",
			src: fakeSource{
				ifaces: []Iface{{Name: "en0", Up: false, Addrs: []netip.Prefix{netip.MustParsePrefix("192.168.1.12/24")}}},
			},
			wantErr: ErrNoCandidate,
		},
		{
			name: "172.16/12 boundaries",
			src: fakeSource{
				ifaces: []Iface{iface("en0", "172.32.0.1/16", "172.16.0.5/16")},
			},
			wantIP:   "172.16.0.5",
			wantNote: "en0",
		},
		{
			name: "ipv6 ignored",
			src: fakeSource{
				ifaces: []Iface{iface("en0", "fe80::1/64")},
			},
			wantErr: ErrNoCandidate,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Discover(tt.src)
			if tt.ambig != nil {
				var ae *AmbiguousError
				if !errors.As(err, &ae) {
					t.Fatalf("want *AmbiguousError, got %v", err)
				}
				if len(ae.Candidates) != len(tt.ambig) {
					t.Fatalf("candidates = %v, want %v", ae.Candidates, tt.ambig)
				}
				for i, c := range tt.ambig {
					if ae.Candidates[i] != c {
						t.Errorf("candidate %d = %v, want %v", i, ae.Candidates[i], c)
					}
				}
				if !strings.Contains(ae.Error(), "--ip") || !strings.Contains(ae.Error(), "en7") {
					t.Errorf("Error() = %q", ae.Error())
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.IP.String() != tt.wantIP {
				t.Errorf("IP = %v, want %v", res.IP, tt.wantIP)
			}
			if tt.wantNote == "" && res.Note != "" {
				t.Errorf("Note = %q, want empty", res.Note)
			}
			if tt.wantNote != "" && !strings.Contains(res.Note, tt.wantNote) {
				t.Errorf("Note = %q, want to contain %q", res.Note, tt.wantNote)
			}
		})
	}
}

func TestPresent(t *testing.T) {
	src := fakeSource{ifaces: []Iface{iface("en0", "192.168.1.12/24"), iface("utun4", "100.64.0.1/32")}}
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.168.1.12", true},
		{"100.64.0.1", true},
		{"192.0.2.1", false},
	}
	for _, tt := range tests {
		got, err := Present(src, netip.MustParseAddr(tt.ip))
		if err != nil || got != tt.want {
			t.Errorf("Present(%s) = %v, %v; want %v", tt.ip, got, err, tt.want)
		}
	}
}

func TestSystemSmoke(t *testing.T) {
	if _, err := System().Interfaces(); err != nil {
		t.Fatal(err)
	}
}
