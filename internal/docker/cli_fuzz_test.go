package docker

import (
	"net"
	"testing"
)

// FuzzParseContainerInfo checks that parsing docker inspect output never panics.
func FuzzParseContainerInfo(f *testing.F) {
	for _, seed := range []string{
		inspectFixture,
		`{"HostConfig":{"NetworkMode":"host","ExtraHosts":["host.docker.internal:host-gateway"]},"NetworkSettings":{"Networks":{"host":{}}}}`,
		`{"Config": {"Cmd": null},"HostConfig": {"NetworkMode": "bridge", "PortBindings": {"8443/tcp": [{"HostIp": "192.168.1.12", "HostPort": "443"}], "80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8080"}]}}}`,
		`{"Config": {"Cmd": null}}`,
		`{}`,
		`null`,
		`not json`,
		``,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = parseContainerInfo(data)
	})
}

// FuzzParseBridgeGateway checks that a gateway it returns is an IPv4 address.
func FuzzParseBridgeGateway(f *testing.F) {
	for _, seed := range []string{
		`[{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]`,
		`[{"Subnet":"fd00::/64","Gateway":"fd00::1"},{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]`,
		`[{"Subnet":"fd00::/64","Gateway":"fd00::1"}]`,
		`[]`,
		`null`,
		`nope`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		gw, err := parseBridgeGateway(data)
		if err != nil {
			return
		}
		if net.ParseIP(gw).To4() == nil {
			t.Fatalf("parseBridgeGateway(%q) = %q, which is not an IPv4 address", data, gw)
		}
	})
}

// FuzzParseContainerStatus checks that it fails exactly when it reports StateAbsent.
func FuzzParseContainerStatus(f *testing.F) {
	for _, seed := range []string{
		"running", "running\n", "restarting", "exited", "created", "paused",
		"dead", "removing", "junk", "",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		st, err := parseContainerStatus(s)
		if (err != nil) != (st == StateAbsent) {
			t.Fatalf("parseContainerStatus(%q) = (%v, %v): error must occur exactly when the state is StateAbsent", s, st, err)
		}
	})
}
