package docker

import (
	"reflect"
	"strings"
	"testing"
)

func TestRunArgsMinimalSpec(t *testing.T) {
	got := strings.Join(RunArgs(ContainerSpec{
		Name:         "c",
		Image:        "img:1",
		PortMappings: []PortMapping{{HostAddr: "127.0.0.1", HostPort: 80, ContainerPort: 8080}},
		Volumes:      []VolumeMount{{VolumeName: "v", ContainerPath: "/data"}},
		Command:      []string{"sh", "-c", "true"},
	}), " ")
	want := "run -d --name c --restart unless-stopped -p 127.0.0.1:80:8080 -v v:/data img:1 sh -c true"
	if got != want {
		t.Errorf("\n got  %s\n want %s", got, want)
	}
}

func TestRunArgsNetworkModeAndExtraHosts(t *testing.T) {
	got := strings.Join(RunArgs(ContainerSpec{
		Name:        "c",
		Image:       "img:1",
		NetworkMode: "host",
		ExtraHosts:  []string{"a:host-gateway", "b:10.0.0.1"},
	}), " ")
	want := "run -d --name c --restart unless-stopped --network host --add-host a:host-gateway --add-host b:10.0.0.1 img:1"
	if got != want {
		t.Errorf("\n got  %s\n want %s", got, want)
	}
}

// inspectFixture is trimmed from `docker inspect caddy_shack` on a native
// Linux engine (Docker 29), for a container created before the relay.
const inspectFixture = `{
  "Config": {"Cmd": ["sh", "-c", "exec caddy run"]},
  "HostConfig": {"NetworkMode": "bridge", "ExtraHosts": null},
  "Mounts": [
    {"Type": "volume", "Name": "caddy_shack_data", "Source": "/var/lib/docker/volumes/caddy_shack_data/_data", "Destination": "/data"},
    {"Type": "bind", "Source": "/etc/x", "Destination": "/x"},
    {"Type": "volume", "Name": "caddy_shack_config", "Source": "/var/lib/docker/volumes/caddy_shack_config/_data", "Destination": "/config"}
  ],
  "NetworkSettings": {"Networks": {
    "caddy_shack": {"Gateway": "172.18.0.1", "IPAddress": "172.18.0.3"},
    "bridge": {"Gateway": "172.17.0.1", "IPAddress": "172.17.0.2"}
  }}
}`

func TestParseContainerInfo(t *testing.T) {
	got, err := parseContainerInfo([]byte(inspectFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := ContainerInfo{
		NetworkMode: "bridge",
		Mounts: []VolumeMount{
			{VolumeName: "caddy_shack_data", ContainerPath: "/data"},
			{VolumeName: "caddy_shack_config", ContainerPath: "/config"},
		},
		Networks: []string{"bridge", "caddy_shack"},
		Command:  []string{"sh", "-c", "exec caddy run"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got  %+v\n want %+v", got, want)
	}
}

func TestParseContainerInfoExtraHostsAndHostMode(t *testing.T) {
	got, err := parseContainerInfo([]byte(`{"HostConfig":{"NetworkMode":"host","ExtraHosts":["host.docker.internal:host-gateway"]},"NetworkSettings":{"Networks":{"host":{}}}}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.NetworkMode != "host" || len(got.ExtraHosts) != 1 || got.ExtraHosts[0] != "host.docker.internal:host-gateway" {
		t.Errorf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Networks, []string{"host"}) {
		t.Errorf("networks: %v", got.Networks)
	}
}

func TestParseContainerInfoRejectsGarbage(t *testing.T) {
	if _, err := parseContainerInfo([]byte("not json")); err == nil {
		t.Error("expected error")
	}
}

func TestIsNoSuchContainer(t *testing.T) {
	for in, want := range map[string]bool{
		"Error response from daemon: No such container: caddy_shack_relay": true,
		"Error: No such object: caddy_shack":                               true,
		"error: no such object: x":                                         true,
		"permission denied while trying to connect":                        false,
	} {
		if got := isNoSuchContainer(in); got != want {
			t.Errorf("%q: got %v want %v", in, got, want)
		}
	}
}

func TestParseBridgeGateway(t *testing.T) {
	cases := []struct {
		in, want string
		wantErr  bool
	}{
		{`[{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]`, "172.17.0.1", false},
		{`[{"Subnet":"fd00::/64","Gateway":"fd00::1"},{"Subnet":"172.17.0.0/16","Gateway":"172.17.0.1"}]`, "172.17.0.1", false},
		{`[{"Subnet":"fd00::/64","Gateway":"fd00::1"}]`, "", true},
		{`[]`, "", true},
		{`null`, "", true},
		{`nope`, "", true},
	}
	for _, tc := range cases {
		got, err := parseBridgeGateway([]byte(tc.in))
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: got (%q, %v), want (%q, err=%v)", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParseContainerInfoPorts(t *testing.T) {
	out := `{
  "Config": {"Cmd": null},
  "HostConfig": {"NetworkMode": "bridge", "PortBindings": {
    "8443/tcp": [{"HostIp": "192.168.1.12", "HostPort": "443"}],
    "80/tcp": [{"HostIp": "127.0.0.1", "HostPort": "8080"}]
  }},
  "Mounts": [],
  "NetworkSettings": {"Networks": {}}
}`
	got, err := parseContainerInfo([]byte(out))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []PortMapping{
		{HostAddr: "127.0.0.1", HostPort: 8080, ContainerPort: 80},
		{HostAddr: "192.168.1.12", HostPort: 443, ContainerPort: 8443},
	}
	if !reflect.DeepEqual(got.Ports, want) {
		t.Errorf("\n got  %+v\n want %+v", got.Ports, want)
	}
}
