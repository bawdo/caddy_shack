package lanip

import (
	"fmt"
	"net"
	"net/netip"
)

type systemSource struct{}

// System returns a Source backed by the real network stack.
func System() Source { return systemSource{} }

// RouteSource asks the kernel which local address it would use to reach
// 192.0.2.1 (TEST-NET-1). UDP dial sends no packet.
func (systemSource) RouteSource() (netip.Addr, error) {
	conn, err := net.Dial("udp4", "192.0.2.1:9")
	if err != nil {
		return netip.Addr{}, err
	}
	defer func() { _ = conn.Close() }()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return netip.Addr{}, fmt.Errorf("unexpected local address type %T", conn.LocalAddr())
	}
	a, ok := netip.AddrFromSlice(ua.IP)
	if !ok {
		return netip.Addr{}, fmt.Errorf("invalid local address %v", ua.IP)
	}
	return a.Unmap(), nil
}

func (systemSource) Interfaces() ([]Iface, error) {
	nis, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]Iface, 0, len(nis))
	for _, ni := range nis {
		i := Iface{
			Name:     ni.Name,
			Up:       ni.Flags&net.FlagUp != 0,
			Loopback: ni.Flags&net.FlagLoopback != 0,
		}
		addrs, err := ni.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok {
				continue
			}
			ones, _ := ipn.Mask.Size()
			ip = ip.Unmap()
			if ones > ip.BitLen() {
				ones = ip.BitLen()
			}
			i.Addrs = append(i.Addrs, netip.PrefixFrom(ip, ones))
		}
		out = append(out, i)
	}
	return out, nil
}
